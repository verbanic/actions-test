terraform {
  backend "local" {}
}

resource "null_resource" "empty" {
  provisioner "local-exec" {
    command = "echo 'TEST!'"
  }
}

module "module" {
  source = "./module"
}
