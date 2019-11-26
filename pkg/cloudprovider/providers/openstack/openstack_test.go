/*
Copyright 2014 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package openstack

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/spf13/pflag"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cloud-provider-openstack/pkg/util/metadata"
)

const (
	testClusterName = "testCluster"
)

func TestReadConfig(t *testing.T) {
	_, err := ReadConfig(nil)
	if err == nil {
		t.Errorf("Should fail when no config is provided: %s", err)
	}

	// Since we are setting env vars, we need to reset old
	// values for other tests to succeed.
	env := clearEnviron(t)
	defer resetEnviron(t, env)

	os.Setenv("OS_PASSWORD", "mypass")
	defer os.Unsetenv("OS_PASSWORD")

	os.Setenv("OS_TENANT_NAME", "admin")
	defer os.Unsetenv("OS_TENANT_NAME")

	cfg, err := ReadConfig(strings.NewReader(`
 [Global]
 auth-url = http://auth.url
 user-id = user
 tenant-name = demo
 tenant-domain-name = Default
 region = RegionOne
 [LoadBalancer]
 create-monitor = yes
 monitor-delay = 1m
 monitor-timeout = 30s
 monitor-max-retries = 3
 [BlockStorage]
 bs-version = auto
 trust-device-path = yes
 ignore-volume-az = yes
 [Metadata]
 search-order = configDrive, metadataService
 `))
	if err != nil {
		t.Fatalf("Should succeed when a valid config is provided: %s", err)
	}
	if cfg.Global.AuthURL != "http://auth.url" {
		t.Errorf("incorrect authurl: %s", cfg.Global.AuthURL)
	}

	if cfg.Global.UserID != "user" {
		t.Errorf("incorrect userid: %s", cfg.Global.UserID)
	}

	if cfg.Global.Password != "mypass" {
		t.Errorf("incorrect password: %s", cfg.Global.Password)
	}

	// config file wins over environment variable
	if cfg.Global.TenantName != "demo" {
		t.Errorf("incorrect tenant name: %s", cfg.Global.TenantName)
	}

	if cfg.Global.TenantDomainName != "Default" {
		t.Errorf("incorrect tenant domain name: %s", cfg.Global.TenantDomainName)
	}

	if cfg.Global.Region != "RegionOne" {
		t.Errorf("incorrect region: %s", cfg.Global.Region)
	}

	if !cfg.LoadBalancer.CreateMonitor {
		t.Errorf("incorrect lb.createmonitor: %t", cfg.LoadBalancer.CreateMonitor)
	}
	if cfg.LoadBalancer.MonitorDelay.Duration != 1*time.Minute {
		t.Errorf("incorrect lb.monitordelay: %s", cfg.LoadBalancer.MonitorDelay)
	}
	if cfg.LoadBalancer.MonitorTimeout.Duration != 30*time.Second {
		t.Errorf("incorrect lb.monitortimeout: %s", cfg.LoadBalancer.MonitorTimeout)
	}
	if cfg.LoadBalancer.MonitorMaxRetries != 3 {
		t.Errorf("incorrect lb.monitormaxretries: %d", cfg.LoadBalancer.MonitorMaxRetries)
	}
	if cfg.BlockStorage.TrustDevicePath != true {
		t.Errorf("incorrect bs.trustdevicepath: %v", cfg.BlockStorage.TrustDevicePath)
	}
	if cfg.BlockStorage.BSVersion != "auto" {
		t.Errorf("incorrect bs.bs-version: %v", cfg.BlockStorage.BSVersion)
	}
	if cfg.BlockStorage.IgnoreVolumeAZ != true {
		t.Errorf("incorrect bs.IgnoreVolumeAZ: %v", cfg.BlockStorage.IgnoreVolumeAZ)
	}
	if cfg.Metadata.SearchOrder != "configDrive, metadataService" {
		t.Errorf("incorrect md.search-order: %v", cfg.Metadata.SearchOrder)
	}
}

func TestReadClouds(t *testing.T) {
	// Clean up env
	env := clearEnviron(t)
	defer resetEnviron(t, env)

	dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		t.Error(err)
	}

	cloudFile := dir + "/test_clouds.yaml"
	_ = os.Remove(cloudFile)

	// Environment setup
	os.Setenv("OS_PASSWORD", "mypass")
	defer os.Unsetenv("OS_PASSWORD")

	os.Setenv("OS_AUTH_URL", "http://wrong-auth.url")
	defer os.Unsetenv("OS_AUTH_URL")

	os.Setenv("OS_CLOUD", "default")
	defer os.Unsetenv("OS_CLOUD")

	var cloud = `
clouds:
  default:
    auth:
      domain_name: default
      auth_url: http://not-auth.url
      project_name: demo
      username: admin
      user_id: user
    region_name: RegionOne
    identity_api_version: 3
`
	data := []byte(cloud)
	err = ioutil.WriteFile(cloudFile, data, 0644)
	if err != nil {
		t.Error(err)
	}
	defer os.Remove(cloudFile)

	cfg, err := ReadConfig(strings.NewReader(`
 [Global]
 auth-url = http://auth.url
 trust-id = mytrust
 use-clouds = true
 clouds-file = ` + cloudFile + `
 [LoadBalancer]
 create-monitor = yes
 monitor-delay = 1m
 monitor-timeout = 30s
 monitor-max-retries = 3
 [BlockStorage]
 bs-version = auto
 trust-device-path = yes
 ignore-volume-az = yes
 [Metadata]
 search-order = configDrive, metadataService
`))

	if err != nil {
		t.Fatalf("Should succeed when a valid config is provided: %s", err)
	}

	// config has priority
	if cfg.Global.AuthURL != "http://auth.url" {
		t.Errorf("incorrect IdentityEndpoint: %s", cfg.Global.AuthURL)
	}

	if cfg.Global.UserID != "user" {
		t.Errorf("incorrect user-id: %s", cfg.Global.UserID)
	}

	if cfg.Global.Username != "admin" {
		t.Errorf("incorrect user-name: %s", cfg.Global.Username)
	}

	if cfg.Global.Password != "mypass" {
		t.Errorf("incorrect password: %s", cfg.Global.Password)
	}

	if cfg.Global.Region != "RegionOne" {
		t.Errorf("incorrect region: %s", cfg.Global.Region)
	}

	if cfg.Global.TenantName != "demo" {
		t.Errorf("incorrect tenant name: %s", cfg.Global.TenantName)
	}

	if cfg.Global.TrustID != "mytrust" {
		t.Errorf("incorrect tenant name: %s", cfg.Global.TrustID)
	}

	// Make non-global sections dont get overwritten
	if cfg.BlockStorage.TrustDevicePath != true {
		t.Errorf("incorrect bs.trustdevicepath: %v", cfg.BlockStorage.TrustDevicePath)
	}
	if cfg.BlockStorage.BSVersion != "auto" {
		t.Errorf("incorrect bs.bs-version: %v", cfg.BlockStorage.BSVersion)
	}
	if !cfg.LoadBalancer.CreateMonitor {
		t.Errorf("incorrect lb.createmonitor: %t", cfg.LoadBalancer.CreateMonitor)
	}
}

func TestToAuthOptions(t *testing.T) {
	cfg := Config{}
	cfg.Global.Username = "user"
	cfg.Global.Password = "pass"
	cfg.Global.DomainName = "local"
	cfg.Global.AuthURL = "http://auth.url"
	cfg.Global.UserID = "user"

	ao := cfg.Global.ToAuthOptions()

	if !ao.AllowReauth {
		t.Errorf("Will need to be able to reauthenticate")
	}
	if ao.Username != cfg.Global.Username {
		t.Errorf("Username %s != %s", ao.Username, cfg.Global.Username)
	}
	if ao.Password != cfg.Global.Password {
		t.Errorf("Password %s != %s", ao.Password, cfg.Global.Password)
	}
	if ao.IdentityEndpoint != cfg.Global.AuthURL {
		t.Errorf("IdentityEndpoint %s != %s", ao.IdentityEndpoint, cfg.Global.AuthURL)
	}
	if ao.UserID != cfg.Global.UserID {
		t.Errorf("UserID %s != %s", ao.UserID, cfg.Global.UserID)
	}
	if ao.Scope.DomainName != cfg.Global.DomainName {
		t.Errorf("DomainName %s != %s", ao.Scope.DomainName, cfg.Global.DomainName)
	}
	if ao.TenantID != cfg.Global.TenantID {
		t.Errorf("TenantID %s != %s", ao.TenantID, cfg.Global.TenantID)
	}

	// test setting of the DomainID
	cfg.Global.DomainID = "2a73b8f597c04551a0fdc8e95544be8a"

	ao = cfg.Global.ToAuthOptions()

	if ao.Scope.DomainID != cfg.Global.DomainID {
		t.Errorf("DomainID %s != %s", ao.Scope.DomainID, cfg.Global.DomainID)
	}
}

func TestCheckOpenStackOpts(t *testing.T) {
	delay := MyDuration{60 * time.Second}
	timeout := MyDuration{30 * time.Second}
	tests := []struct {
		name          string
		openstackOpts *OpenStack
		expectedError error
	}{
		{
			name: "test1",
			openstackOpts: &OpenStack{
				provider: nil,
				lbOpts: LoadBalancerOpts{
					LBVersion:            "v2",
					SubnetID:             "6261548e-ffde-4bc7-bd22-59c83578c5ef",
					FloatingNetworkID:    "38b8b5f9-64dc-4424-bf86-679595714786",
					LBMethod:             "ROUND_ROBIN",
					LBProvider:           "haproxy",
					CreateMonitor:        true,
					MonitorDelay:         delay,
					MonitorTimeout:       timeout,
					MonitorMaxRetries:    uint(3),
					ManageSecurityGroups: true,
				},
				metadataOpts: MetadataOpts{
					SearchOrder: metadata.ConfigDriveID,
				},
			},
			expectedError: nil,
		},
		{
			name: "test2",
			openstackOpts: &OpenStack{
				provider: nil,
				lbOpts: LoadBalancerOpts{
					LBVersion:            "v2",
					FloatingNetworkID:    "38b8b5f9-64dc-4424-bf86-679595714786",
					LBMethod:             "ROUND_ROBIN",
					CreateMonitor:        true,
					MonitorDelay:         delay,
					MonitorTimeout:       timeout,
					MonitorMaxRetries:    uint(3),
					ManageSecurityGroups: true,
				},
				metadataOpts: MetadataOpts{
					SearchOrder: metadata.ConfigDriveID,
				},
			},
			expectedError: nil,
		},
		{
			name: "test3",
			openstackOpts: &OpenStack{
				provider: nil,
				lbOpts: LoadBalancerOpts{
					LBVersion:            "v2",
					SubnetID:             "6261548e-ffde-4bc7-bd22-59c83578c5ef",
					FloatingNetworkID:    "38b8b5f9-64dc-4424-bf86-679595714786",
					LBMethod:             "ROUND_ROBIN",
					CreateMonitor:        true,
					MonitorTimeout:       timeout,
					MonitorMaxRetries:    uint(3),
					ManageSecurityGroups: true,
				},
				metadataOpts: MetadataOpts{
					SearchOrder: metadata.ConfigDriveID,
				},
			},
			expectedError: fmt.Errorf("monitor-delay not set in cloud provider config"),
		},
		{
			name: "test4",
			openstackOpts: &OpenStack{
				provider: nil,
				metadataOpts: MetadataOpts{
					SearchOrder: "",
				},
			},
			expectedError: fmt.Errorf("invalid value in section [Metadata] with key `search-order`. Value cannot be empty"),
		},
		{
			name: "test5",
			openstackOpts: &OpenStack{
				provider: nil,
				metadataOpts: MetadataOpts{
					SearchOrder: "value1,value2,value3",
				},
			},
			expectedError: fmt.Errorf("invalid value in section [Metadata] with key `search-order`. Value cannot contain more than 2 elements"),
		},
		{
			name: "test6",
			openstackOpts: &OpenStack{
				provider: nil,
				metadataOpts: MetadataOpts{
					SearchOrder: "value1",
				},
			},
			expectedError: fmt.Errorf("invalid element %q found in section [Metadata] with key `search-order`."+
				"Supported elements include %q and %q", "value1", metadata.ConfigDriveID, metadata.MetadataID),
		},
		{
			name: "test7",
			openstackOpts: &OpenStack{
				provider: nil,
				lbOpts: LoadBalancerOpts{
					LBVersion:            "v2",
					SubnetID:             "6261548e-ffde-4bc7-bd22-59c83578c5ef",
					FloatingNetworkID:    "38b8b5f9-64dc-4424-bf86-679595714786",
					LBMethod:             "ROUND_ROBIN",
					CreateMonitor:        true,
					MonitorDelay:         delay,
					MonitorTimeout:       timeout,
					ManageSecurityGroups: true,
				},
				metadataOpts: MetadataOpts{
					SearchOrder: metadata.ConfigDriveID,
				},
			},
			expectedError: fmt.Errorf("monitor-max-retries not set in cloud provider config"),
		},
		{
			name: "test8",
			openstackOpts: &OpenStack{
				provider: nil,
				lbOpts: LoadBalancerOpts{
					LBVersion:            "v2",
					SubnetID:             "6261548e-ffde-4bc7-bd22-59c83578c5ef",
					FloatingNetworkID:    "38b8b5f9-64dc-4424-bf86-679595714786",
					LBMethod:             "ROUND_ROBIN",
					CreateMonitor:        true,
					MonitorDelay:         delay,
					MonitorMaxRetries:    uint(3),
					ManageSecurityGroups: true,
				},
				metadataOpts: MetadataOpts{
					SearchOrder: metadata.ConfigDriveID,
				},
			},
			expectedError: fmt.Errorf("monitor-timeout not set in cloud provider config"),
		},
	}

	for _, testcase := range tests {
		err := checkOpenStackOpts(testcase.openstackOpts)

		if err == nil && testcase.expectedError == nil {
			continue
		}
		if (err != nil && testcase.expectedError == nil) || (err == nil && testcase.expectedError != nil) || err.Error() != testcase.expectedError.Error() {
			t.Errorf("%s failed: expected err=%q, got %q",
				testcase.name, testcase.expectedError, err)
		}
	}
}

func TestCaller(t *testing.T) {
	called := false
	myFunc := func() { called = true }

	c := newCaller()
	c.call(myFunc)

	if !called {
		t.Errorf("caller failed to call function in default case")
	}

	c.disarm()
	called = false
	c.call(myFunc)

	if called {
		t.Error("caller still called function when disarmed")
	}

	// Confirm the "usual" deferred caller pattern works as expected

	called = false
	successCase := func() {
		c := newCaller()
		defer c.call(func() { called = true })
		c.disarm()
	}
	if successCase(); called {
		t.Error("Deferred success case still invoked unwind")
	}

	called = false
	failureCase := func() {
		c := newCaller()
		defer c.call(func() { called = true })
	}
	if failureCase(); !called {
		t.Error("Deferred failure case failed to invoke unwind")
	}
}

// An arbitrary sort.Interface, just for easier comparison
type AddressSlice []v1.NodeAddress

func (a AddressSlice) Len() int           { return len(a) }
func (a AddressSlice) Less(i, j int) bool { return a[i].Address < a[j].Address }
func (a AddressSlice) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

func TestNodeAddresses(t *testing.T) {
	srv := servers.Server{
		Status:     "ACTIVE",
		HostID:     "29d3c8c896a45aa4c34e52247875d7fefc3d94bbcc9f622b5d204362",
		AccessIPv4: "50.56.176.99",
		AccessIPv6: "2001:4800:790e:510:be76:4eff:fe04:82a8",
		Addresses: map[string]interface{}{
			"private": []interface{}{
				map[string]interface{}{
					"OS-EXT-IPS-MAC:mac_addr": "fa:16:3e:7c:1b:2b",
					"version":                 float64(4),
					"addr":                    "10.0.0.32",
					"OS-EXT-IPS:type":         "fixed",
				},
				map[string]interface{}{
					"version":         float64(4),
					"addr":            "50.56.176.36",
					"OS-EXT-IPS:type": "floating",
				},
				map[string]interface{}{
					"version": float64(4),
					"addr":    "10.0.0.31",
					// No OS-EXT-IPS:type
				},
			},
			"public": []interface{}{
				map[string]interface{}{
					"version": float64(4),
					"addr":    "50.56.176.35",
				},
				map[string]interface{}{
					"version": float64(6),
					"addr":    "2001:4800:780e:510:be76:4eff:fe04:84a8",
				},
			},
		},
		Metadata: map[string]string{
			"name":       "a1-yinvcez57-0-bvynoyawrhcg-kube-minion-fg5i4jwcc2yy",
			TypeHostName: "a1-yinvcez57-0-bvynoyawrhcg-kube-minion-fg5i4jwcc2yy.novalocal",
		},
	}

	networkingOpts := NetworkingOpts{
		PublicNetworkName: "public",
	}

	addrs, err := nodeAddresses(&srv, networkingOpts)
	if err != nil {
		t.Fatalf("nodeAddresses returned error: %v", err)
	}

	sort.Sort(AddressSlice(addrs))
	t.Logf("addresses is %v", addrs)

	want := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "10.0.0.31"},
		{Type: v1.NodeInternalIP, Address: "10.0.0.32"},
		{Type: v1.NodeExternalIP, Address: "2001:4800:780e:510:be76:4eff:fe04:84a8"},
		{Type: v1.NodeExternalIP, Address: "2001:4800:790e:510:be76:4eff:fe04:82a8"},
		{Type: v1.NodeExternalIP, Address: "50.56.176.35"},
		{Type: v1.NodeExternalIP, Address: "50.56.176.36"},
		{Type: v1.NodeExternalIP, Address: "50.56.176.99"},
		{Type: v1.NodeHostName, Address: "a1-yinvcez57-0-bvynoyawrhcg-kube-minion-fg5i4jwcc2yy.novalocal"},
	}

	if !reflect.DeepEqual(want, addrs) {
		t.Errorf("nodeAddresses returned incorrect value %v", addrs)
	}
}

func TestNodeAddressesCustomPublicNetwork(t *testing.T) {
	srv := servers.Server{
		Status:     "ACTIVE",
		HostID:     "29d3c8c896a45aa4c34e52247875d7fefc3d94bbcc9f622b5d204362",
		AccessIPv4: "50.56.176.99",
		AccessIPv6: "2001:4800:790e:510:be76:4eff:fe04:82a8",
		Addresses: map[string]interface{}{
			"private": []interface{}{
				map[string]interface{}{
					"OS-EXT-IPS-MAC:mac_addr": "fa:16:3e:7c:1b:2b",
					"version":                 float64(4),
					"addr":                    "10.0.0.32",
					"OS-EXT-IPS:type":         "fixed",
				},
				map[string]interface{}{
					"version":         float64(4),
					"addr":            "50.56.176.36",
					"OS-EXT-IPS:type": "floating",
				},
				map[string]interface{}{
					"version": float64(4),
					"addr":    "10.0.0.31",
					// No OS-EXT-IPS:type
				},
			},
			"pub-net": []interface{}{
				map[string]interface{}{
					"version": float64(4),
					"addr":    "50.56.176.35",
				},
				map[string]interface{}{
					"version": float64(6),
					"addr":    "2001:4800:780e:510:be76:4eff:fe04:84a8",
				},
			},
		},
	}

	networkingOpts := NetworkingOpts{
		PublicNetworkName: "pub-net",
	}

	addrs, err := nodeAddresses(&srv, networkingOpts)
	if err != nil {
		t.Fatalf("nodeAddresses returned error: %v", err)
	}

	sort.Sort(AddressSlice(addrs))
	t.Logf("addresses is %v", addrs)

	want := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "10.0.0.31"},
		{Type: v1.NodeInternalIP, Address: "10.0.0.32"},
		{Type: v1.NodeExternalIP, Address: "2001:4800:780e:510:be76:4eff:fe04:84a8"},
		{Type: v1.NodeExternalIP, Address: "2001:4800:790e:510:be76:4eff:fe04:82a8"},
		{Type: v1.NodeExternalIP, Address: "50.56.176.35"},
		{Type: v1.NodeExternalIP, Address: "50.56.176.36"},
		{Type: v1.NodeExternalIP, Address: "50.56.176.99"},
	}

	if !reflect.DeepEqual(want, addrs) {
		t.Errorf("nodeAddresses returned incorrect value %v", addrs)
	}
}

func TestNodeAddressesIPv6Disabled(t *testing.T) {
	srv := servers.Server{
		Status:     "ACTIVE",
		HostID:     "29d3c8c896a45aa4c34e52247875d7fefc3d94bbcc9f622b5d204362",
		AccessIPv4: "50.56.176.99",
		AccessIPv6: "2001:4800:790e:510:be76:4eff:fe04:82a8",
		Addresses: map[string]interface{}{
			"private": []interface{}{
				map[string]interface{}{
					"OS-EXT-IPS-MAC:mac_addr": "fa:16:3e:7c:1b:2b",
					"version":                 float64(4),
					"addr":                    "10.0.0.32",
					"OS-EXT-IPS:type":         "fixed",
				},
				map[string]interface{}{
					"version":         float64(4),
					"addr":            "50.56.176.36",
					"OS-EXT-IPS:type": "floating",
				},
				map[string]interface{}{
					"version": float64(4),
					"addr":    "10.0.0.31",
					// No OS-EXT-IPS:type
				},
			},
			"public": []interface{}{
				map[string]interface{}{
					"version": float64(4),
					"addr":    "50.56.176.35",
				},
				map[string]interface{}{
					"version": float64(6),
					"addr":    "2001:4800:780e:510:be76:4eff:fe04:84a8",
				},
			},
		},
	}

	networkingOpts := NetworkingOpts{
		PublicNetworkName:   "public",
		IPv6SupportDisabled: true,
	}

	addrs, err := nodeAddresses(&srv, networkingOpts)
	if err != nil {
		t.Fatalf("nodeAddresses returned error: %v", err)
	}

	sort.Sort(AddressSlice(addrs))
	t.Logf("addresses is %v", addrs)

	want := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "10.0.0.31"},
		{Type: v1.NodeInternalIP, Address: "10.0.0.32"},
		{Type: v1.NodeExternalIP, Address: "50.56.176.35"},
		{Type: v1.NodeExternalIP, Address: "50.56.176.36"},
		{Type: v1.NodeExternalIP, Address: "50.56.176.99"},
	}

	if !reflect.DeepEqual(want, addrs) {
		t.Errorf("nodeAddresses returned incorrect value %v", addrs)
	}
}

func TestNewOpenStack(t *testing.T) {
	cfg := ConfigFromEnv()
	testConfigFromEnv(t, &cfg)

	_, err := NewOpenStack(cfg)
	if err != nil {
		t.Fatalf("Failed to construct/authenticate OpenStack: %s", err)
	}
}

func TestLoadBalancer(t *testing.T) {
	cfg := ConfigFromEnv()
	testConfigFromEnv(t, &cfg)

	versions := []string{"v2"}

	for _, v := range versions {
		t.Logf("Trying LBVersion = '%s'\n", v)
		cfg.LoadBalancer.LBVersion = v

		os, err := NewOpenStack(cfg)
		if err != nil {
			t.Fatalf("Failed to construct/authenticate OpenStack: %s", err)
		}

		lb, ok := os.LoadBalancer()
		if !ok {
			t.Fatalf("LoadBalancer() returned false - perhaps your stack doesn't support Neutron?")
		}

		_, exists, err := lb.GetLoadBalancer(context.TODO(), testClusterName, &v1.Service{ObjectMeta: metav1.ObjectMeta{Name: "noexist"}})
		if err != nil {
			t.Fatalf("GetLoadBalancer(\"noexist\") returned error: %s", err)
		}
		if exists {
			t.Fatalf("GetLoadBalancer(\"noexist\") returned exists")
		}
	}
}

var FakeMetadata = metadata.Metadata{
	UUID:             "83679162-1378-4288-a2d4-70e13ec132aa",
	Name:             "test",
	AvailabilityZone: "nova",
}

func TestZones(t *testing.T) {
	metadata.Set(&FakeMetadata)
	defer metadata.Clear()

	os := OpenStack{
		provider: &gophercloud.ProviderClient{
			IdentityBase: "http://auth.url/",
		},
		region: "myRegion",
	}

	z, ok := os.Zones()
	if !ok {
		t.Fatalf("Zones() returned false")
	}

	zone, err := z.GetZone(context.TODO())
	if err != nil {
		t.Fatalf("GetZone() returned error: %s", err)
	}

	if zone.Region != "myRegion" {
		t.Fatalf("GetZone() returned wrong region (%s)", zone.Region)
	}

	if zone.FailureDomain != "nova" {
		t.Fatalf("GetZone() returned wrong failure domain (%s)", zone.FailureDomain)
	}
}

func TestInstanceIDFromProviderID(t *testing.T) {
	testCases := []struct {
		providerID string
		instanceID string
		fail       bool
	}{
		{
			providerID: ProviderName + "://" + "/" + "7b9cf879-7146-417c-abfd-cb4272f0c935",
			instanceID: "7b9cf879-7146-417c-abfd-cb4272f0c935",
			fail:       false,
		},
		{
			providerID: "openstack://7b9cf879-7146-417c-abfd-cb4272f0c935",
			instanceID: "",
			fail:       true,
		},
		{
			providerID: "7b9cf879-7146-417c-abfd-cb4272f0c935",
			instanceID: "",
			fail:       true,
		},
		{
			providerID: "other-provider:///7b9cf879-7146-417c-abfd-cb4272f0c935",
			instanceID: "",
			fail:       true,
		},
	}

	for _, test := range testCases {
		instanceID, err := instanceIDFromProviderID(test.providerID)
		if (err != nil) != test.fail {
			t.Errorf("%s yielded `err != nil` as %t. expected %t", test.providerID, (err != nil), test.fail)
		}

		if test.fail {
			continue
		}

		if instanceID != test.instanceID {
			t.Errorf("%s yielded %s. expected %s", test.providerID, instanceID, test.instanceID)
		}
	}
}

func TestToAuth3Options(t *testing.T) {
	cfg := Config{}
	cfg.Global.Username = "user"
	cfg.Global.Password = "pass"
	cfg.Global.DomainID = "2a73b8f597c04551a0fdc8e95544be8a"
	cfg.Global.DomainName = "local"
	cfg.Global.AuthURL = "http://auth.url"
	cfg.Global.UserID = "user"
	cfg.Global.TenantName = "demo"
	cfg.Global.TenantDomainName = "Default"

	ao := cfg.Global.ToAuth3Options()

	if !ao.AllowReauth {
		t.Errorf("Will need to be able to reauthenticate")
	}
	if ao.Username != cfg.Global.Username {
		t.Errorf("Username %s != %s", ao.Username, cfg.Global.Username)
	}
	if ao.Password != cfg.Global.Password {
		t.Errorf("Password %s != %s", ao.Password, cfg.Global.Password)
	}
	if ao.DomainID != cfg.Global.DomainID {
		t.Errorf("DomainID %s != %s", ao.DomainID, cfg.Global.DomainID)
	}
	if ao.IdentityEndpoint != cfg.Global.AuthURL {
		t.Errorf("IdentityEndpoint %s != %s", ao.IdentityEndpoint, cfg.Global.AuthURL)
	}
	if ao.UserID != cfg.Global.UserID {
		t.Errorf("UserID %s != %s", ao.UserID, cfg.Global.UserID)
	}
	if ao.DomainName != cfg.Global.DomainName {
		t.Errorf("DomainName %s != %s", ao.DomainName, cfg.Global.DomainName)
	}
	if ao.Scope.ProjectName != cfg.Global.TenantName {
		t.Errorf("TenantName %s != %s", ao.Scope.ProjectName, cfg.Global.TenantName)
	}
	if ao.Scope.DomainName != cfg.Global.TenantDomainName {
		t.Errorf("TenantDomainName %s != %s", ao.Scope.DomainName, cfg.Global.TenantDomainName)
	}
}

func TestToAuth3OptionsScope(t *testing.T) {
	// Use Domain Name/ID if Tenant Domain Name/ID is not set
	cfg := Config{}
	cfg.Global.Username = "user"
	cfg.Global.Password = "pass"
	cfg.Global.DomainID = "2a73b8f597c04551a0fdc8e95544be8a"
	cfg.Global.DomainName = "local"
	cfg.Global.AuthURL = "http://auth.url"
	cfg.Global.UserID = "user"
	cfg.Global.TenantName = "demo"

	ao := cfg.Global.ToAuth3Options()

	if ao.Scope.ProjectName != cfg.Global.TenantName {
		t.Errorf("TenantName %s != %s", ao.Scope.ProjectName, cfg.Global.TenantName)
	}
	if ao.Scope.DomainName != cfg.Global.DomainName {
		t.Errorf("DomainName %s != %s", ao.Scope.DomainName, cfg.Global.DomainName)
	}
	if ao.Scope.DomainID != cfg.Global.DomainID {
		t.Errorf("DomainID %s != %s", ao.Scope.DomainID, cfg.Global.DomainID)
	}

	// Use Tenant Domain Name/ID if set
	cfg = Config{}
	cfg.Global.Username = "user"
	cfg.Global.Password = "pass"
	cfg.Global.DomainID = "2a73b8f597c04551a0fdc8e95544be8a"
	cfg.Global.DomainName = "local"
	cfg.Global.AuthURL = "http://auth.url"
	cfg.Global.UserID = "user"
	cfg.Global.TenantName = "demo"
	cfg.Global.TenantDomainName = "Default"
	cfg.Global.TenantDomainID = "default"

	ao = cfg.Global.ToAuth3Options()

	if ao.Scope.ProjectName != cfg.Global.TenantName {
		t.Errorf("TenantName %s != %s", ao.Scope.ProjectName, cfg.Global.TenantName)
	}
	if ao.Scope.DomainName != cfg.Global.TenantDomainName {
		t.Errorf("TenantDomainName %s != %s", ao.Scope.DomainName, cfg.Global.TenantDomainName)
	}
	if ao.Scope.DomainID != cfg.Global.TenantDomainID {
		t.Errorf("TenantDomainID %s != %s", ao.Scope.DomainName, cfg.Global.TenantDomainID)
	}

	// Do not use neither Domain Name nor ID, if Tenant ID was provided
	cfg = Config{}
	cfg.Global.Username = "user"
	cfg.Global.Password = "pass"
	cfg.Global.DomainID = "2a73b8f597c04551a0fdc8e95544be8a"
	cfg.Global.DomainName = "local"
	cfg.Global.AuthURL = "http://auth.url"
	cfg.Global.UserID = "user"
	cfg.Global.TenantID = "7808db451cfc43eaa9acda7d67da8cf1"
	cfg.Global.TenantDomainName = "Default"
	cfg.Global.TenantDomainID = "default"

	ao = cfg.Global.ToAuth3Options()

	if ao.Scope.ProjectName != "" {
		t.Errorf("TenantName in the scope  is not empty")
	}
	if ao.Scope.DomainName != "" {
		t.Errorf("DomainName in the scope is not empty")
	}
	if ao.Scope.DomainID != "" {
		t.Errorf("DomainID in the scope is not empty")
	}
}

func TestUserAgentFlag(t *testing.T) {
	tests := []struct {
		name        string
		shouldParse bool
		flags       []string
		expected    []string
	}{
		{"no_flag", true, []string{}, nil},
		{"one_flag", true, []string{"--user-agent=cluster/abc-123"}, []string{"cluster/abc-123"}},
		{"multiple_flags", true, []string{"--user-agent=a/b", "--user-agent=c/d"}, []string{"a/b", "c/d"}},
		{"flag_with_space", true, []string{"--user-agent=a b"}, []string{"a b"}},
		{"flag_split_with_space", true, []string{"--user-agent=a", "b"}, []string{"a"}},
		{"empty_flag", false, []string{"--user-agent"}, nil},
	}

	for _, testCase := range tests {
		userAgentData = []string{}

		t.Run(testCase.name, func(t *testing.T) {
			fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
			AddExtraFlags(fs)

			err := fs.Parse(testCase.flags)

			if testCase.shouldParse && err != nil {
				t.Errorf("Flags failed to parse")
			} else if !testCase.shouldParse && err == nil {
				t.Errorf("Flags should not have parsed")
			} else if testCase.shouldParse {
				if !reflect.DeepEqual(userAgentData, testCase.expected) {
					t.Errorf("userAgentData %#v did not match expected value %#v", userAgentData, testCase.expected)
				}
			}
		})
	}
}

func clearEnviron(t *testing.T) []string {
	env := os.Environ()
	for _, pair := range env {
		if strings.HasPrefix(pair, "OS_") {
			i := strings.Index(pair, "=") + 1
			os.Unsetenv(pair[:i-1])
		}
	}
	return env
}
func resetEnviron(t *testing.T, items []string) {
	for _, pair := range items {
		if strings.HasPrefix(pair, "OS_") {
			i := strings.Index(pair, "=") + 1
			if err := os.Setenv(pair[:i-1], pair[i:]); err != nil {
				t.Errorf("Setenv(%q, %q) failed during reset: %v", pair[:i-1], pair[i:], err)
			}
		}
	}
}

func testConfigFromEnv(t *testing.T, cfg *Config) {
	if cfg.Global.AuthURL == "" {
		t.Skip("No config found in environment")
	}
}
