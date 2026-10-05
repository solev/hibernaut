package e2b_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/engine/e2b/e2bapi"
)

func TestGeneratedClientsCannotForkNatively(t *testing.T) {
	for _, client := range []reflect.Type{
		reflect.TypeFor[*e2bapi.Client](),
		reflect.TypeFor[*e2bapi.ClientWithResponses](),
	} {
		t.Run(client.String(), func(t *testing.T) {
			for i := 0; i < client.NumMethod(); i++ {
				if method := client.Method(i); strings.Contains(method.Name, "Fork") {
					t.Errorf("%s exposes native fork method %s", client, method.Name)
				}
			}
		})
	}
}
