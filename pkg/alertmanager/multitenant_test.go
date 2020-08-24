// +build !race

package alertmanager

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-kit/kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/weaveworks/common/user"

	"github.com/cortexproject/cortex/pkg/alertmanager/alerts"
	"github.com/cortexproject/cortex/pkg/util/flagext"
)

var (
	simpleConfigOne = `route:
  receiver: dummy

receivers:
  - name: dummy`

	simpleConfigTwo = `route:
  receiver: dummy

receivers:
  - name: dummy`
)

// basic easily configurable mock
type mockAlertStore struct {
	configs map[string]alerts.AlertConfigDesc
}

func (m *mockAlertStore) ListAlertConfigs(ctx context.Context) (map[string]alerts.AlertConfigDesc, error) {
	return m.configs, nil
}

func (m *mockAlertStore) GetAlertConfig(ctx context.Context, user string) (alerts.AlertConfigDesc, error) {
	return alerts.AlertConfigDesc{}, fmt.Errorf("not implemented")
}

func (m *mockAlertStore) SetAlertConfig(ctx context.Context, cfg alerts.AlertConfigDesc) error {
	return fmt.Errorf("not implemented")
}

func (m *mockAlertStore) DeleteAlertConfig(ctx context.Context, user string) error {
	return fmt.Errorf("not implemented")
}

// TestLoadAllConfigs ensures the multitenant alertmanager can properly load configs from a local backend store.
// It is excluded from the race detector due to a vendored race issue https://github.com/prometheus/alertmanager/issues/2182
func TestLoadAllConfigs(t *testing.T) {
	mockStore := &mockAlertStore{
		configs: map[string]alerts.AlertConfigDesc{
			"user1": {
				User:      "user1",
				RawConfig: simpleConfigOne,
				Templates: []*alerts.TemplateDesc{},
			},
			"user2": {
				User:      "user2",
				RawConfig: simpleConfigOne,
				Templates: []*alerts.TemplateDesc{},
			},
		},
	}

	externalURL := flagext.URLValue{}
	err := externalURL.Set("http://localhost/api/prom")
	require.NoError(t, err)

	tempDir, err := ioutil.TempDir(os.TempDir(), "alertmanager")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	reg := prometheus.NewPedanticRegistry()
	am := createMultitenantAlertmanager(&MultitenantAlertmanagerConfig{
		ExternalURL: externalURL,
		DataDir:     tempDir,
	}, nil, nil, mockStore, log.NewNopLogger(), reg)

	// Ensure the configs are synced correctly
	require.NoError(t, am.updateConfigs())
	require.Len(t, am.alertmanagers, 2)

	currentConfig, exists := am.cfgs["user1"]
	require.True(t, exists)
	require.Equal(t, simpleConfigOne, currentConfig.RawConfig)

	assert.NoError(t, testutil.GatherAndCompare(reg, bytes.NewBufferString(`
		# HELP cortex_alertmanager_config_invalid Whenever the Alertmanager config is invalid for a user.
		# TYPE cortex_alertmanager_config_invalid gauge
		cortex_alertmanager_config_invalid{user="user1"} 0
		cortex_alertmanager_config_invalid{user="user2"} 0
	`), "cortex_alertmanager_config_invalid"))

	// Ensure when a 3rd config is added, it is synced correctly
	mockStore.configs["user3"] = alerts.AlertConfigDesc{
		User:      "user3",
		RawConfig: simpleConfigOne,
		Templates: []*alerts.TemplateDesc{},
	}

	require.NoError(t, am.updateConfigs())
	require.Len(t, am.alertmanagers, 3)

	assert.NoError(t, testutil.GatherAndCompare(reg, bytes.NewBufferString(`
		# HELP cortex_alertmanager_config_invalid Whenever the Alertmanager config is invalid for a user.
		# TYPE cortex_alertmanager_config_invalid gauge
		cortex_alertmanager_config_invalid{user="user1"} 0
		cortex_alertmanager_config_invalid{user="user2"} 0
		cortex_alertmanager_config_invalid{user="user3"} 0
	`), "cortex_alertmanager_config_invalid"))

	// Ensure the config is updated
	mockStore.configs["user1"] = alerts.AlertConfigDesc{
		User:      "user1",
		RawConfig: simpleConfigTwo,
		Templates: []*alerts.TemplateDesc{},
	}

	require.NoError(t, am.updateConfigs())

	currentConfig, exists = am.cfgs["user1"]
	require.True(t, exists)
	require.Equal(t, simpleConfigTwo, currentConfig.RawConfig)

	// Test Delete User, ensure config is remove but alertmananger
	// exists and is set to inactive
	delete(mockStore.configs, "user3")
	require.NoError(t, am.updateConfigs())
	currentConfig, exists = am.cfgs["user3"]
	require.False(t, exists)
	require.Equal(t, "", currentConfig.RawConfig)

	userAM, exists := am.alertmanagers["user3"]
	require.True(t, exists)
	require.False(t, userAM.IsActive())

	assert.NoError(t, testutil.GatherAndCompare(reg, bytes.NewBufferString(`
		# HELP cortex_alertmanager_config_invalid Whenever the Alertmanager config is invalid for a user.
		# TYPE cortex_alertmanager_config_invalid gauge
		cortex_alertmanager_config_invalid{user="user1"} 0
		cortex_alertmanager_config_invalid{user="user2"} 0
	`), "cortex_alertmanager_config_invalid"))

	// Ensure when a 3rd config is re-added, it is synced correctly
	mockStore.configs["user3"] = alerts.AlertConfigDesc{
		User:      "user3",
		RawConfig: simpleConfigOne,
		Templates: []*alerts.TemplateDesc{},
	}

	require.NoError(t, am.updateConfigs())

	currentConfig, exists = am.cfgs["user3"]
	require.True(t, exists)
	require.Equal(t, simpleConfigOne, currentConfig.RawConfig)

	userAM, exists = am.alertmanagers["user3"]
	require.True(t, exists)
	require.True(t, userAM.IsActive())

	assert.NoError(t, testutil.GatherAndCompare(reg, bytes.NewBufferString(`
		# HELP cortex_alertmanager_config_invalid Whenever the Alertmanager config is invalid for a user.
		# TYPE cortex_alertmanager_config_invalid gauge
		cortex_alertmanager_config_invalid{user="user1"} 0
		cortex_alertmanager_config_invalid{user="user2"} 0
		cortex_alertmanager_config_invalid{user="user3"} 0
	`), "cortex_alertmanager_config_invalid"))
}

func TestAlertmanager_NoExternalURL(t *testing.T) {
	tempDir, err := ioutil.TempDir(os.TempDir(), "alertmanager")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create the Multitenant Alertmanager.
	reg := prometheus.NewPedanticRegistry()
	_, err = NewMultitenantAlertmanager(&MultitenantAlertmanagerConfig{
		DataDir: tempDir,
	}, log.NewNopLogger(), reg)

	require.EqualError(t, err, "unable to create Alertmanager because the external URL has not been configured")
}

func TestAlertmanager_ServeHTTP(t *testing.T) {
	mockStore := &mockAlertStore{
		configs: map[string]alerts.AlertConfigDesc{},
	}

	externalURL := flagext.URLValue{}
	err := externalURL.Set("http://localhost:8080/alertmanager")
	require.NoError(t, err)

	tempDir, err := ioutil.TempDir(os.TempDir(), "alertmanager")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create the Multitenant Alertmanager.
	reg := prometheus.NewPedanticRegistry()
	am := createMultitenantAlertmanager(&MultitenantAlertmanagerConfig{
		ExternalURL: externalURL,
		DataDir:     tempDir,
	}, nil, nil, mockStore, log.NewNopLogger(), reg)

	// Request when no user configuration is present.
	req := httptest.NewRequest("GET", externalURL.String(), nil)
	req.Header.Add(user.OrgIDHeaderName, "user1")
	w := httptest.NewRecorder()

	am.ServeHTTP(w, req)

	resp := w.Result()
	body, _ := ioutil.ReadAll(resp.Body)
	require.Equal(t, "the Alertmanager is not configured\n", string(body))

	// Create a configuration for the user in storage.
	mockStore.configs["user1"] = alerts.AlertConfigDesc{
		User:      "user1",
		RawConfig: simpleConfigTwo,
		Templates: []*alerts.TemplateDesc{},
	}

	// Make the alertmanager pick it up, then pause it.
	err = am.updateConfigs()
	require.NoError(t, err)
	am.alertmanagers["user1"].Pause()

	// Request when user configuration is paused.
	w = httptest.NewRecorder()
	am.ServeHTTP(w, req)

	resp = w.Result()
	body, _ = ioutil.ReadAll(resp.Body)
	require.Equal(t, "the Alertmanager is not configured\n", string(body))
}