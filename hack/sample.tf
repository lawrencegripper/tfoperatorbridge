provider "azurerm" {
  version = "~> 2.16"
  features {}
}


resource "azurerm_resource_group" "example" {
  name     = "example"
  location = "West Europe"
}
