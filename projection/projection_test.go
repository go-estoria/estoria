package projection_test

import (
	"strings"
	"testing"

	"github.com/go-estoria/estoria/projection"
)

func TestID_String(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		id   projection.ID
		want string
	}{
		{"simple name", projection.ID{Name: "orders", Version: 7}, "orders_v7"},
		{"name with digits and underscores", projection.ID{Name: "order_items2", Version: 12}, "order_items2_v12"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.id.String(); got != tt.want {
				t.Errorf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestID_Validate(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		id      projection.ID
		wantErr bool
	}{
		{"simple name", projection.ID{Name: "orders", Version: 1}, false},
		{"name with digits and underscores", projection.ID{Name: "order_items2", Version: 3}, false},
		{"name at the maximum length", projection.ID{Name: strings.Repeat("a", projection.MaxNameLength), Version: 1}, false},
		{"empty name", projection.ID{Version: 1}, true},
		{"name over the maximum length", projection.ID{Name: strings.Repeat("a", projection.MaxNameLength+1), Version: 1}, true},
		{"name starting with a digit", projection.ID{Name: "1orders", Version: 1}, true},
		{"name starting with an underscore", projection.ID{Name: "_orders", Version: 1}, true},
		{"name starting with an uppercase letter", projection.ID{Name: "Orders", Version: 1}, true},
		{"name ending with an underscore", projection.ID{Name: "orders_", Version: 1}, true},
		{"name containing an uppercase letter", projection.ID{Name: "orderItems", Version: 1}, true},
		{"name containing a hyphen", projection.ID{Name: "order-items", Version: 1}, true},
		{"name containing a dot", projection.ID{Name: "order.items", Version: 1}, true},
		{"zero version", projection.ID{Name: "orders"}, true},
		{"negative version", projection.ID{Name: "orders", Version: -1}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.id.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("want an error validating %+v, got nil", tt.id)
			} else if !tt.wantErr && err != nil {
				t.Errorf("want %+v to validate, got %v", tt.id, err)
			}
		})
	}
}
