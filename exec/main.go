// Package main is the main entrypoint to the application.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/abcxyz/pkg/logging"
	"github.com/google/go-github/v59/github"
	"github.com/google/uuid"
	"github.com/sethvargo/go-retry"
)

var newline = regexp.MustCompile("\r?\n")

type ClientPayload struct {
	UUID     uuid.UUID `json:"uuid"`
	Commands []string  `json:"commands"`
}

func main() {
	ctx, done := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer done()

	if err := realMain(ctx); err != nil {
		done()
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func realMain(ctx context.Context) error {
	ctx = logging.WithLogger(ctx, logging.NewFromEnv(""))
	logger := logging.FromContext(ctx)

	if len(os.Args) > 1 {
		return fmt.Errorf("invalid args")
	}

	fmt.Printf("Type the commands you want to run, press CTRL+D to exit\n\n")

	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("failed to read stdin: %w", err)
	}
	commands := newline.Split(string(stdin), -1)

	client := github.NewClient(nil).WithAuthToken(os.Getenv("GITHUB_TOKEN"))

	dispatchID := uuid.New()

	user, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return fmt.Errorf("failed to get authenticated user: %w", err)
	}

	payload := &ClientPayload{
		UUID:     dispatchID,
		Commands: commands,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to create client_payload: %w", err)
	}

	p := json.RawMessage(data)

	ghOwner := "verbanic"
	ghRepo := "actions-test"
	workflowFilename := "dispatch.yml"

	fmt.Println("########### DISPATCH REQUEST #########")

	dispatchRequest := github.DispatchRequestOptions{EventType: "test", ClientPayload: &p}
	if _, _, err = client.Repositories.Dispatch(ctx, ghOwner, ghRepo, dispatchRequest); err != nil {
		return fmt.Errorf("failed to create repository dispatch: %w", err)
	}

	var foundRun *github.WorkflowRun
	if err := withRetries(ctx, func(ctx context.Context) error {
		listRunsRequest := &github.ListWorkflowRunsOptions{Actor: user.GetLogin(), Event: "repository_dispatch"}
		runs, _, err := client.Actions.ListWorkflowRunsByFileName(ctx, ghOwner, ghRepo, workflowFilename, listRunsRequest)
		if err != nil {
			return retry.RetryableError(fmt.Errorf("failed to list workflow runs: %w", err))
		}

		for _, run := range runs.WorkflowRuns {
			if run.GetName() == fmt.Sprintf("dispatch_test[%s]", dispatchID) {
				foundRun = run
				break
			}
		}

		if foundRun == nil {
			return retry.RetryableError(fmt.Errorf("failed to find workflow run"))
		}

		return nil
	}); err != nil {
		return err
	}

	fmt.Printf("User: %s\n", user.GetLogin())
	fmt.Printf("UUID: %s\n", dispatchID)
	fmt.Printf("URL : %s/actions/runs/%d\n", foundRun.Repository.GetHTMLURL(), foundRun.GetID())

	var completedRun *github.WorkflowRun
	if err := withRetries(ctx, func(ctx context.Context) error {
		run, _, err := client.Actions.GetWorkflowRunByID(ctx, ghOwner, ghRepo, foundRun.GetID())
		if err != nil {
			return retry.RetryableError(fmt.Errorf("failed to list workflow runs: %w", err))
		}

		if run.GetStatus() != "completed" {
			return retry.RetryableError(fmt.Errorf("workflow run not completed"))
		}

		completedRun = run

		return nil
	}); err != nil {
		return err
	}

	fmt.Println("########### JOB CONCLUSION ###########")
	fmt.Printf("JOB: %s\n", completedRun.GetConclusion())

	url, _, err := client.Actions.GetWorkflowRunAttemptLogs(ctx, ghOwner, ghRepo, completedRun.GetID(), completedRun.GetRunAttempt(), 2)
	if err != nil {
		return fmt.Errorf("failed to get workflow run logs url: %w", err)
	}

	resp, err := http.Get(url.String())
	if err != nil {
		return fmt.Errorf("failed to download workflow run logs: %w", err)
	}

	logData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read logs response: %w", err)
	}
	logReader := bytes.NewReader(logData)

	zr, err := zip.NewReader(logReader, logReader.Size())
	if err != nil {
		return fmt.Errorf("failed to read zip data: %w", err)
	}

	for _, f := range zr.File {
		logger.DebugContext(ctx, "found zip file", "file", f.Name)
		if f.Name == "plan/5_Validate.txt" || f.Name == "plan/7_Command.txt" {
			v, err := f.Open()
			if err != nil {
				return fmt.Errorf("failed to open zip file: %w", err)
			}
			defer v.Close()

			b, err := io.ReadAll(v)
			if err != nil {
				return fmt.Errorf("failed to read log file: %w", err)
			}

			re := regexp.MustCompile(fmt.Sprintf(`(?m)[0-9]{4}\-[0-9]{2}\-[0-9]{2}T[0-9]{2}\:[0-9]{2}\:[0-9]{2}\.[0-9]{7}Z \[START\-%s\](?s)(?P<output>.*)[0-9]{4}\-[0-9]{2}\-[0-9]{2}T[0-9]{2}\:[0-9]{2}\:[0-9]{2}\.[0-9]{7}Z \[END\-%s\]`, dispatchID, dispatchID))
			m := re.FindSubmatch(b)
			if len(m) == 0 {
				return fmt.Errorf("failed to find output text")
			}

			var matchData []byte
			for i := range re.SubexpNames() {
				if i == 0 {
					continue
				}
				matchData = m[i]
			}
			fmt.Println("########### COMMAND OUTPUT ###########")
			fmt.Println(string(matchData))
		}
	}

	return nil
}

func withRetries(ctx context.Context, retryFunc retry.RetryFunc) error {
	backoff := retry.NewConstant(5 * time.Second)
	backoff = retry.WithMaxDuration(10*time.Minute, backoff)

	if err := retry.Do(ctx, backoff, retryFunc); err != nil {
		return fmt.Errorf("failed to execute retriable function: %w", err)
	}
	return nil
}
