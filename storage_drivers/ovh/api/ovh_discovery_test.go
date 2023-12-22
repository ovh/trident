package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/jarcoal/httpmock"
	"github.com/ovh/go-ovh/ovh"
	"github.com/stretchr/testify/assert"

	"github.com/netapp/trident/storage"
)

func getFakeSDK() *Client {
	config := ClientConfig{
		ClientID:       "id",
		ClientSecret:   "secret",
		ClientLocation: "ovh-eu",
		Location:       "eu-west-rbx",
	}

	httpClient, _ := ovh.NewOAuth2Client(config.ClientLocation, config.ClientID, config.ClientSecret)
	client := &Client{
		config: &config,
		sdkClient: &OVHEFSClient{
			httpClient: httpClient,
		},
	}

	// Capacity pools
	client.sdkClient.CapacityPoolMap = make(map[string]*CapacityPool)

	cPool1 := &CapacityPool{
		ID:           "bc1f340a-6ee2-41e3-9ce5-5bcbed05b625",
		Name:         fmt.Sprintf("%s_%s", "bc1f340a-6ee2-41e3-9ce5-5bcbed05b625", "bc1f340a-6ee2-41e3-9ce5-5bcbed05b625"),
		Region:       "eu-west-rbx",
		ServiceLevel: PerformanceLevelPremium,
		Status:       "available",
	}
	client.sdkClient.CapacityPoolMap[cPool1.Name] = cPool1

	cPool2 := &CapacityPool{
		ID:           "bd68abb4-cc20-4ece-99ac-8d40e2d487f1",
		Name:         fmt.Sprintf("%s_%s", "bd68abb4-cc20-4ece-99ac-8d40e2d487f1", "bd68abb4-cc20-4ece-99ac-8d40e2d487f1"),
		Region:       "eu-west-rbx",
		ServiceLevel: PerformanceLevelPremium,
		Status:       "available",
	}
	client.sdkClient.CapacityPoolMap[cPool2.Name] = cPool2

	cPool3 := &CapacityPool{
		ID:           "0c7f3bf5-4f65-4b49-9c37-2870bf181a7d",
		Name:         fmt.Sprintf("%s_%s", "0c7f3bf5-4f65-4b49-9c37-2870bf181a7d", "0c7f3bf5-4f65-4b49-9c37-2870bf181a7d"),
		Region:       "eu-west-rbx",
		ServiceLevel: PerformanceLevelPremium,
		Status:       "available",
	}
	client.sdkClient.CapacityPoolMap[cPool3.Name] = cPool3

	cPool4 := &CapacityPool{
		ID:           "4088247d-902a-4bca-96bb-480bbb74de233",
		Name:         fmt.Sprintf("%s_%s", "4088247d-902a-4bca-96bb-480bbb74de233", "4088247d-902a-4bca-96bb-480bbb74de233"),
		Region:       "eu-west-rbx",
		ServiceLevel: PerformanceLevelPremium,
		Status:       "available",
	}
	client.sdkClient.CapacityPoolMap[cPool4.Name] = cPool4

	return client
}

func mockListStoragePoolsResponse() {
	mockGetOAuth2TokenResponse()

	storagePools := []*CapacityPool{
		{
			ID:           "1234-1234-1234-1234",
			Name:         "1234-1234-1234-1234",
			Region:       "eu-west-rbx",
			ServiceLevel: "premium",
			Status:       "available",
		},
	}
	resp, _ := json.Marshal(storagePools)
	httpmock.RegisterResponder("GET", fmt.Sprint("https://eu.api.ovh.com/1.0/storage/netapp"), httpmock.NewBytesResponder(http.StatusOK, resp))
}

func TestDiscoverOVHResources(t *testing.T) {
	httpmock.Activate()
	client := getFakeSDK()

	mockListStoragePoolsResponse()

	err := client.DiscoverOVHResources(ctx)
	// result, err := client.discoverCapacityPools(ctx)

	expectedCapacityPoolsPools := map[string]*CapacityPool{
		"1234-1234-1234-1234": {
			ID:           "1234-1234-1234-1234",
			Name:         "1234-1234-1234-1234",
			Region:       "eu-west-rbx",
			ServiceLevel: "Premium",
			Status:       "available",
		},
	}
	/*
		expectedPools := []*CapacityPool{
			{
				ID:           "1234-1234-1234-1234",
				Name:         "1234-1234-1234-1234_1234-1234-1234-1234",
				Region:       "eu-west-rbx",
				ServiceLevel: "premium",
				Status:       "available",
			},
		}
	*/

	assert.NoError(t, err)
	//	assert.Equal(t, &expectedPools, result)
	assert.Equal(t, 1, len(client.sdkClient.OVHResources.CapacityPoolMap))
	assert.Equal(t, expectedCapacityPoolsPools, client.sdkClient.OVHResources.CapacityPoolMap)

	httpmock.DeactivateAndReset()
}

func TestDiscoverCapacityPools(t *testing.T) {
	sdk := getFakeSDK()

	httpmock.Activate()
	mockListStoragePoolsResponse()

	result, err := sdk.discoverCapacityPools(ctx)

	httpmock.DeactivateAndReset()

	assert.NoError(t, err)
	//	assert.Equal(t, &expectedPools, result)
	assert.Equal(t, 1, len(*result))
}

func mockListStoragePoolsFailedResponse() {
	mockGetOAuth2TokenResponse()

	httpmock.RegisterResponder("GET", fmt.Sprint("https://eu.api.ovh.com/1.0/storage/netapp"), httpmock.NewBytesResponder(http.StatusInternalServerError, nil))
}

func TestDiscoverCapacityPools_GetPoolsFailed(t *testing.T) {
	sdk := getFakeSDK()

	httpmock.Activate()
	mockListStoragePoolsFailedResponse()

	result, err := sdk.discoverCapacityPools(ctx)

	httpmock.DeactivateAndReset()

	assert.Error(t, err)
	assert.Nil(t, result)
}

func mockListStoragePoolsEmptyResponse() {
	mockGetOAuth2TokenResponse()

	storagePools := []*CapacityPool{}
	resp, _ := json.Marshal(storagePools)
	httpmock.RegisterResponder("GET", fmt.Sprint("https://eu.api.ovh.com/1.0/storage/netapp"), httpmock.NewBytesResponder(http.StatusOK, resp))
}

func TestDiscoverCapacityPools_EmptyPoolsList(t *testing.T) {
	sdk := getFakeSDK()

	httpmock.Activate()
	mockListStoragePoolsEmptyResponse()

	result, err := sdk.discoverCapacityPools(ctx)

	httpmock.DeactivateAndReset()

	assert.Error(t, err)
	assert.Nil(t, result)
}

func mockListStoragePoolsNoMatchingPoolsResponse() {
	mockGetOAuth2TokenResponse()

	storagePools := []*CapacityPool{
		{
			ID:           "1234-1234-1234-1234",
			Name:         "1234-1234-1234-1234",
			Region:       "eu-west-gra",
			ServiceLevel: "premium",
			Status:       "available",
		},
	}
	resp, _ := json.Marshal(storagePools)
	httpmock.RegisterResponder("GET", fmt.Sprint("https://eu.api.ovh.com/1.0/storage/netapp"), httpmock.NewBytesResponder(http.StatusOK, resp))
}

func TestDiscoverCapacityPools_NoMatchingPools(t *testing.T) {
	sdk := getFakeSDK()

	httpmock.Activate()
	mockListStoragePoolsNoMatchingPoolsResponse()

	result, err := sdk.discoverCapacityPools(ctx)

	httpmock.DeactivateAndReset()

	assert.Error(t, err)
	assert.Nil(t, result)
}

func TestCheckForUnsatisfiedPools_NoPools(t *testing.T) {
	sPool1 := storage.NewStoragePool(nil, "pool1")
	sPool2 := storage.NewStoragePool(nil, "pool2")

	sdk := getFakeSDK()
	sdk.sdkClient.StoragePoolMap = map[string]storage.Pool{"pool1": sPool1, "pool2": sPool2}

	result := sdk.checkForUnsatisfiedPools(ctx)

	assert.Zero(t, len(result), "expected no errors")
}

func TestCheckForUnsatisfiedPools_EmptyPools(t *testing.T) {
	sPool1 := storage.NewStoragePool(nil, "pool1")
	sPool1.InternalAttributes()[capacityPools] = ""
	sPool2 := storage.NewStoragePool(nil, "pool2")
	sPool2.InternalAttributes()[capacityPools] = ""

	sdk := getFakeSDK()
	sdk.sdkClient.StoragePoolMap = map[string]storage.Pool{"pool1": sPool1, "pool2": sPool2}

	result := sdk.checkForUnsatisfiedPools(ctx)

	assert.Zero(t, len(result), "expected no errors")
}

func TestCheckForUnsatisfiedPools_ValidPools(t *testing.T) {
	sPool1 := storage.NewStoragePool(nil, "pool1")
	sPool1.InternalAttributes()[capacityPools] = "bc1f340a-6ee2-41e3-9ce5-5bcbed05b625_bc1f340a-6ee2-41e3-9ce5-5bcbed05b625,bd68abb4-cc20-4ece-99ac-8d40e2d487f1_bd68abb4-cc20-4ece-99ac-8d40e2d487f1"
	sPool2 := storage.NewStoragePool(nil, "pool2")
	sPool2.InternalAttributes()[capacityPools] = "0c7f3bf5-4f65-4b49-9c37-2870bf181a7d_0c7f3bf5-4f65-4b49-9c37-2870bf181a7d,4088247d-902a-4bca-96bb-480bbb74de23_4088247d-902a-4bca-96bb-480bbb74de23"

	sdk := getFakeSDK()
	sdk.sdkClient.StoragePoolMap = map[string]storage.Pool{"pool1": sPool1, "pool2": sPool2}

	result := sdk.checkForUnsatisfiedPools(ctx)

	assert.Zero(t, len(result), "expected no errors")
}

func TestCheckForUnsatisfiedPools_OneInvalidPool(t *testing.T) {
	sPool1 := storage.NewStoragePool(nil, "pool1")
	sPool1.InternalAttributes()[capacityPools] = "bc1f340a-6ee2-41e3-9ce5-5bcbed05b625_bc1f340a-6ee2-41e3-9ce5-5bcbed05b625,bd68abb4-cc20-4ece-99ac-8d40e2d487f1_bd68abb4-cc20-4ece-99ac-8d40e2d487f1"
	sPool2 := storage.NewStoragePool(nil, "pool2")
	sPool2.InternalAttributes()[capacityPools] = "af418bc6-f43c-4364-bfa7-b306eeb129d7"

	sdk := getFakeSDK()
	sdk.sdkClient.StoragePoolMap = map[string]storage.Pool{"pool1": sPool1, "pool2": sPool2}

	result := sdk.checkForUnsatisfiedPools(ctx)

	assert.Equal(t, 1, len(result), "expected no error")
}

func TestCheckForNonexistentCapacityPools_NoPools(t *testing.T) {
	sdk := getFakeSDK()
	sdk.sdkClient.StoragePoolMap = make(map[string]storage.Pool)

	result := sdk.checkForNonexistentCapacityPools(ctx)

	assert.False(t, result, "expected no error")
}

func TestCheckForNonexistentCapacityPools_Empty(t *testing.T) {
	sPool := storage.NewStoragePool(nil, "pool")
	sPool.InternalAttributes()[capacityPools] = ""

	sdk := getFakeSDK()
	sdk.sdkClient.StoragePoolMap = map[string]storage.Pool{"pool": sPool}

	result := sdk.checkForNonexistentCapacityPools(ctx)

	assert.False(t, result, "expected no error")
}

func TestCheckForNonexistentCapacityPools_OK(t *testing.T) {
	sPool := storage.NewStoragePool(nil, "pool")
	sPool.InternalAttributes()[capacityPools] = "bc1f340a-6ee2-41e3-9ce5-5bcbed05b625_bc1f340a-6ee2-41e3-9ce5-5bcbed05b625,bd68abb4-cc20-4ece-99ac-8d40e2d487f1_bd68abb4-cc20-4ece-99ac-8d40e2d487f1"

	sdk := getFakeSDK()
	sdk.sdkClient.StoragePoolMap = map[string]storage.Pool{"pool": sPool}

	result := sdk.checkForNonexistentCapacityPools(ctx)
	assert.False(t, result, "expected no error")
}

func TestCheckForNonexistentCapacityPools_Missing(t *testing.T) {
	sPool := storage.NewStoragePool(nil, "pool")
	sPool.InternalAttributes()[capacityPools] = "bc1f340a-6ee2-41e3-9ce5-5bcbed05b625_bc1f340a-6ee2-41e3-9ce5-5bcbed05b625,bd68abb4-cc20-4ece-99ac-8d40e2d487f1_bd68abb4-cc20-4ece-99ac-8d40e2d487f1,a072a3fe-2b18-4495-aca6-0d09b3636354"

	sdk := getFakeSDK()
	sdk.sdkClient.StoragePoolMap = map[string]storage.Pool{"pool": sPool}

	result := sdk.checkForNonexistentCapacityPools(ctx)

	assert.True(t, result, "expected error")
}

func TestCapacityPools(t *testing.T) {
	sdk := getFakeSDK()
	sdk.sdkClient.StoragePoolMap = make(map[string]storage.Pool)

	expected := &[]*CapacityPool{
		sdk.capacityPool("bc1f340a-6ee2-41e3-9ce5-5bcbed05b625_bc1f340a-6ee2-41e3-9ce5-5bcbed05b625"),
		sdk.capacityPool("bd68abb4-cc20-4ece-99ac-8d40e2d487f1_bd68abb4-cc20-4ece-99ac-8d40e2d487f1"),
		sdk.capacityPool("0c7f3bf5-4f65-4b49-9c37-2870bf181a7d_0c7f3bf5-4f65-4b49-9c37-2870bf181a7d"),
		sdk.capacityPool("4088247d-902a-4bca-96bb-480bbb74de233_4088247d-902a-4bca-96bb-480bbb74de233"),
	}

	actual := sdk.CapacityPools()

	assert.ElementsMatch(t, *expected, *actual)
}

func TestCapacityPoolsForStoragePools(t *testing.T) {
	sdk := getFakeSDK()
	sdk.sdkClient.StoragePoolMap = make(map[string]storage.Pool)

	cPool1 := sdk.capacityPool("bc1f340a-6ee2-41e3-9ce5-5bcbed05b625_bc1f340a-6ee2-41e3-9ce5-5bcbed05b625")
	cPool2 := sdk.capacityPool("bd68abb4-cc20-4ece-99ac-8d40e2d487f1_bd68abb4-cc20-4ece-99ac-8d40e2d487f1")

	sPool1 := storage.NewStoragePool(nil, "testPool1")
	sPool1.InternalAttributes()[capacityPools] = "0c7f3bf5-4f65-4b49-9c37-2870bf181a7d"
	sdk.sdkClient.StoragePoolMap[sPool1.Name()] = sPool1

	sPool2 := storage.NewStoragePool(nil, "testPool2")
	sPool2.InternalAttributes()[capacityPools] = "bc1f340a-6ee2-41e3-9ce5-5bcbed05b625_bc1f340a-6ee2-41e3-9ce5-5bcbed05b625,bd68abb4-cc20-4ece-99ac-8d40e2d487f1_bd68abb4-cc20-4ece-99ac-8d40e2d487f1"
	sdk.sdkClient.StoragePoolMap[sPool2.Name()] = sPool2

	expected := []*CapacityPool{cPool1, cPool2}

	actual := sdk.CapacityPoolsForStoragePools(ctx)

	assert.ElementsMatch(t, expected, actual)
}

func TestCapacityPoolsForStoragePool(t *testing.T) {
	sdk := getFakeSDK()

	cPool1 := sdk.capacityPool("bc1f340a-6ee2-41e3-9ce5-5bcbed05b625_bc1f340a-6ee2-41e3-9ce5-5bcbed05b625")
	cPool2 := sdk.capacityPool("bd68abb4-cc20-4ece-99ac-8d40e2d487f1_bd68abb4-cc20-4ece-99ac-8d40e2d487f1")
	cPool3 := sdk.capacityPool("0c7f3bf5-4f65-4b49-9c37-2870bf181a7d_0c7f3bf5-4f65-4b49-9c37-2870bf181a7d")
	cPool4 := sdk.capacityPool("4088247d-902a-4bca-96bb-480bbb74de233_4088247d-902a-4bca-96bb-480bbb74de233")

	sPool := storage.NewStoragePool(nil, "testPool")

	tests := []struct {
		capacityPools string
		serviceLevel  string
		expected      []*CapacityPool
	}{
		{
			capacityPools: "",
			serviceLevel:  "Premium",
			expected:      []*CapacityPool{cPool1, cPool2, cPool3, cPool4},
		},

		{
			capacityPools: "bc1f340a-6ee2-41e3-9ce5-5bcbed05b625_bc1f340a-6ee2-41e3-9ce5-5bcbed05b625",
			serviceLevel:  "Premium",
			expected:      []*CapacityPool{cPool1},
		},

		{
			capacityPools: "",
			serviceLevel:  "Ultra",
			expected:      []*CapacityPool{},
		},
	}

	for _, test := range tests {
		sPool.InternalAttributes()[capacityPools] = test.capacityPools

		cPools := sdk.CapacityPoolsForStoragePool(ctx, sPool, test.serviceLevel)

		assert.ElementsMatch(t, test.expected, cPools)
	}
}

func TestEnsureVolumeInValidCapacityPool(t *testing.T) {
	sdk := getFakeSDK()
	sdk.sdkClient.StoragePoolMap = make(map[string]storage.Pool)

	volume := &Volume{
		ServiceID:     "bc1f340a-6ee2-41e3-9ce5-5bcbed05b625",
		CreationToken: "pvc-3bfdd474-2417-4b6e-980b-f1ff43c7f5fa",
	}

	sPool := storage.NewStoragePool(nil, "testPool")
	sPool.InternalAttributes()[capacityPools] = "bc1f340a-6ee2-41e3-9ce5-5bcbed05b625_bc1f340a-6ee2-41e3-9ce5-5bcbed05b625"

	sdk.sdkClient.StoragePoolMap[sPool.Name()] = sPool

	assert.Nil(t, sdk.EnsureVolumeInValidCapacityPool(ctx, volume), "result not nil")
}

func TestEnsureVolumeInValidCapacityPool_NoPool(t *testing.T) {
	sdk := getFakeSDK()
	sdk.sdkClient.StoragePoolMap = make(map[string]storage.Pool)

	volume := &Volume{
		ServiceID:     "bc1f340a-6ee2-41e3-9ce5-5bcbed05b625",
		CreationToken: "pvc-3bfdd474-2417-4b6e-980b-f1ff43c7f5fa",
	}

	assert.Nil(t, sdk.EnsureVolumeInValidCapacityPool(ctx, volume), "result not nil")
}

func TestEnsureVolumeInValidCapacityPool_BadPool(t *testing.T) {
	sdk := getFakeSDK()
	sdk.sdkClient.StoragePoolMap = make(map[string]storage.Pool)

	volume := &Volume{
		ServiceID:     "bc1f340a-6ee2-41e3-9ce5-5bcbed05b625",
		CreationToken: "pvc-3bfdd474-2417-4b6e-980b-f1ff43c7f5fa",
	}

	sPool := storage.NewStoragePool(nil, "testPool")
	sPool.InternalAttributes()[capacityPools] = "bd68abb4-cc20-4ece-99ac-8d40e2d487f1_bd68abb4-cc20-4ece-99ac-8d40e2d487f1"

	sdk.sdkClient.StoragePoolMap[sPool.Name()] = sPool

	result := sdk.EnsureVolumeInValidCapacityPool(ctx, volume)

	assert.NotNil(t, result, "result nil")
}
