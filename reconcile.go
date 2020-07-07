package main

import (
	"log"

	"github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/providers"
	"github.com/zclconf/go-cty/cty"
)

func reconcile(provider *plugin.GRPCProvider) {
	providerConfigBlock := provider.GetSchema().Provider.Block
	// providerConfigBlock.Attributes["bob"] = "bill"
	// config := providers.ConfigureRequest{
	// 	TerraformVersion: "0.12.15",
	// 	Config: &terraform.ResourceConfig{
	// 		Raw: map[string]interface{}{"bar": "baz"},
	// 	},
	// 	// Config: cty.ObjectVal(map[string]cty.Value{
	// 	// 	"tenant_id":                    cty.StringVal("Ermintrude"),
	// 	// 	"client_id":                    cty.StringVal("Ermintrude"),
	// 	// 	"client_secret":                cty.StringVal("Ermintrude"),
	// 	// 	"subscription_id":              cty.StringVal("Ermintrude"),
	// 	// 	"features":                     cty.MapValEmpty(cty.String),
	// 	// 	"skip_credentials_validation":  cty.BoolVal(true),
	// 	// 	"disable_terraform_partner_id": cty.BoolVal(false),

	// 	// 	"auxiliary_tenant_ids":           cty.EmptyObjectVal,
	// 	// 	"client_certificate_password":    cty.EmptyObjectVal,
	// 	// 	"client_certificate_path":        cty.EmptyObjectVal,
	// 	// 	"disable_correlation_request_id": cty.EmptyObjectVal,
	// 	// 	"environment":                    cty.EmptyObjectVal,
	// 	// 	"msi_endpoint":                   cty.EmptyObjectVal,
	// 	// 	"partner_id":                     cty.EmptyObjectVal,
	// 	// 	"skip_provider_registration":     cty.EmptyObjectVal,
	// 	// 	"storage_use_azuread":            cty.EmptyObjectVal,
	// 	// 	"use_msi":                        cty.BoolVal(false),
	// 	// }),
	// }

	// cty.Value{

	// }

	// ctyValue := cty.ObjectVal(map[string]cty.Value{
	// 	"tenant_id":       cty.StringVal("Ermintrude"),
	// 	"client_id":       cty.StringVal("Ermintrude"),
	// 	"client_secret":   cty.StringVal("Ermintrude"),
	// 	"subscription_id": cty.StringVal("Ermintrude"),
	// 	"features":        cty.ObjectVal(map[string]cty.Value{}),
	// })

	configProvider := providerConfigBlock.EmptyValue()

	// provider "azurerm" {
	// 	# whilst the `version` attribute is optional, we recommend pinning to a given version of the Provider
	// 	version = "=2.0.0"
	// 	features {}
	// 	features nil
	// }

	// configProvider.
	featuresType := providerConfigBlock.BlockTypes["features"]
	// featuresBlockEmptyVal := featuresType.EmptyValue()
	featuresBlockMap := map[string]cty.Value{}

	log.Println(featuresType)
	for name, nestedBlock := range featuresType.BlockTypes {
		featuresBlockMap[name] = nestedBlock.EmptyValue()
	}

	configValueMap := configProvider.AsValueMap()
	// newEmptyValueOfTheListElement := configValueMap["features"].Type().ListElementType()
	configValueMap["features"] = cty.ListVal([]cty.Value{cty.ObjectVal(featuresBlockMap)})
	// configValueMap

	configFull := cty.ObjectVal(configValueMap)

	log.Println(configFull)

	resp := provider.PrepareProviderConfig(providers.PrepareProviderConfigRequest{
		Config: configFull,
	})
	log.Println(resp)

	if resp.Diagnostics.Err() != nil {
		log.Println(resp.Diagnostics.Err().Error())
	}

	resp2 := provider.Configure(providers.ConfigureRequest{
		Config: resp.PreparedConfig,
	})
	if resp2.Diagnostics.Err() != nil {
		log.Println(resp2.Diagnostics.Err().Error())
	}
	// // https://github.com/hashicorp/terraform/blob/86e6481cc661b2ba694d353aab6d11838c066c7c/plugin/resource_provider_test.go
	// err := provider.Configure(&terraform.ResourceConfig{
	// 	Raw: map[string]interface{}{"bar": "baz"},
	// })
	// if err != nil {
	// 	panic(err)
	// }

	// readResp := provider.ReadDataSource(providers.ReadDataSourceRequest{
	// 	TypeName: "azurerm_subscription",
	// 	Config: cty.ObjectVal(map[string]cty.Value{
	// 		"display_name": cty.StringVal("test"),
	// 		"id":           cty.StringVal("bob"),
	// 		"state":        cty.ObjectVal(map[string]cty.Value{}),
	// 	}),
	// })

	// if readResp.Diagnostics.Err() != nil {
	// 	log.Println(readResp.Diagnostics.Err().Error())
	// 	panic("end")
	// }
}
