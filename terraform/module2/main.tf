resource "null_resource" "empty" {
  provisioner "local-exec" {
    command = "echo 'TEST!'"
  }
}
