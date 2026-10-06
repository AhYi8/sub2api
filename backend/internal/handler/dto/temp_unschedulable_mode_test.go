package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountDTOMapsTempUnschedulableMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		credentials map[string]any
		want        string
	}{
		{name: "explicit inherit", credentials: map[string]any{"temp_unschedulable_mode": "inherit"}, want: "inherit"},
		{name: "explicit override", credentials: map[string]any{"temp_unschedulable_mode": "override"}, want: "override"},
		{name: "explicit disabled", credentials: map[string]any{"temp_unschedulable_mode": "disabled"}, want: "disabled"},
		{name: "legacy enabled", credentials: map[string]any{"temp_unschedulable_enabled": true}, want: "override"},
		{name: "legacy disabled", credentials: map[string]any{"temp_unschedulable_enabled": false}, want: "disabled"},
		{name: "no legacy key", credentials: map[string]any{}, want: "inherit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &service.Account{Credentials: tc.credentials}
			full := AccountFromServiceShallow(account)
			lite := AccountListItemFromAccount(full)
			require.Equal(t, tc.want, full.TempUnschedulableMode)
			require.Equal(t, tc.want, lite.TempUnschedulableMode)
		})
	}
}
