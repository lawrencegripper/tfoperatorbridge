provider "azurerm" {
  version = "~> 2.16"
  features {}
}

provider "azuread" {
  version = "~> 0.11"
  features {}
}

provider "aws" {
  version = "~> 2.70.0"
  features {}
}


provider "helm" {
  version = "~> 1.2.3"
  features {}
}


resource "azurerm_resource_group" "example" {
  name     = "example"
  location = "West Europe"
}
