package guardian

import rego.v1

import input.event.sender as sender

deny contains msg if {
	allowed_users := {"octocat"}
	not sender.login in allowed_users
	msg := sprintf("permission denied: user %s is not allowed", [sender.login])
}
