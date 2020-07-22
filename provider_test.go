package main

import (
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func Test_createEmptyProviderConfWithDefaults(t *testing.T) {

	tests := []struct {
		name         string
		providerName string
		want         *cty.Value
		wantErr      bool
	}{
		{
			name:         "azurerm",
			providerName: "azurerm",
			want:         &cty.EmptyObjectVal,
			wantErr:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			provider := getInstanceOfProvider(tt.providerName)

			_, err := createEmptyProviderConfWithDefaults(provider)
			if (err != nil) != tt.wantErr {
				t.Errorf("createEmptyProviderConfWithDefaults() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			// if !reflect.DeepEqual(got, tt.want) {
			// 	t.Errorf("createEmptyProviderConfWithDefaults() = %v, want %v", got, tt.want)
			// }
		})
	}
}
