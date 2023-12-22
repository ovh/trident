package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jarcoal/httpmock"
	"github.com/ovh/go-ovh/ovh"

	"github.com/maxatome/tdhttpmock"
	"github.com/stretchr/testify/assert"

	. "github.com/netapp/trident/logging"
)

const (
	MockTime         = 1457018875
	MockAPIURL       = "https://eu.api.ovh.com/1.0/storage/netapp"
	MockServiceID    = "2e4b6044-d43c-4864-b280-684af2e3147a"
	MockVolumeID     = "9f14094d-56b9-4aed-bd2b-4e1c14640c89"
	MockSnapshotID   = "d25a555e-6339-48a7-afb4-a5650d763b67"
	MockExportRuleID = "c749188d-0e9e-4b52-b5a8-7cc07f9d2445"
)

var ctx = context.TODO()

func TestMain(m *testing.M) {
	// Disable any standard log output
	InitLogOutput(io.Discard)
	code := m.Run()
	os.Exit(code)
}

func getOVHClient() *Client {
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
	cPool := &CapacityPool{
		ID:           MockServiceID,
		Name:         MockServiceID,
		Region:       "eu-west-rbx",
		ServiceLevel: "premium",
		Status:       "available",
	}
	client.sdkClient.CapacityPoolMap[cPool.Name] = cPool

	return client
}

func TestNewDriver(t *testing.T) {
	config := ClientConfig{
		ClientID:       "id",
		ClientSecret:   "secret",
		ClientLocation: "ovh-eu",
		Location:       "eu-west-rbx",
	}

	d, err := NewDriver(&config)

	assert.Nil(t, err)
	assert.NotNil(t, d, "Driver is empty.")
}

func TestCreateVolumeID(t *testing.T) {
	actual := CreateVolumeID("899879fb-73ad-48fe-b3cc-a963a27e1050", "b2525764-c2ed-4e9d-a94b-0c0795c0f275")

	expected := "/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share/b2525764-c2ed-4e9d-a94b-0c0795c0f275"

	assert.Equal(t, expected, actual, "volume IDs not equal")
}

func TestCreateVolumeFullName(t *testing.T) {
	actual := CreateVolumeFullName("899879fb-73ad-48fe-b3cc-a963a27e1050", "pvc-26037574-c18e-4454-a7cb-62e4edd3d4c3")

	expected := "/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share/pvc-26037574-c18e-4454-a7cb-62e4edd3d4c3"

	assert.Equal(t, expected, actual, "volume full names not equal")
}

func TestParseVolumeID(t *testing.T) {
	capacityPool, volume, err := ParseVolumeID("/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share/b2525764-c2ed-4e9d-a94b-0c0795c0f275")

	assert.Equal(t, "899879fb-73ad-48fe-b3cc-a963a27e1050", capacityPool, "capacity pool not correct")
	assert.Equal(t, "b2525764-c2ed-4e9d-a94b-0c0795c0f275", volume, "volume not correct")
	assert.NoError(t, err, "error is not nil")
}

func TestParseVolumeIDError(t *testing.T) {
	tests := []struct {
		description string
		input       string
	}{
		{
			"no capacity pool value",
			"/storage/netapp/share/b2525764-c2ed-4e9d-a94b-0c0795c0f275",
		},
		{
			"no volume key",
			"/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/b2525764-c2ed-4e9d-a94b-0c0795c0f275",
		},
		{
			"no volume value",
			"/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share",
		},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("Parse volume ID error: %s", test.description), func(t *testing.T) {
			_, _, err := ParseVolumeID(test.input)
			assert.Error(t, err, test.description)
		})
	}
}

func TestParseVolumeName(t *testing.T) {
	capacityPool, volumeName, err := ParseVolumeName("/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share/pvc-26037574-c18e-4454-a7cb-62e4edd3d4c3")

	assert.Equal(t, "899879fb-73ad-48fe-b3cc-a963a27e1050", capacityPool, "capacity pool not correct")
	assert.Equal(t, "pvc-26037574-c18e-4454-a7cb-62e4edd3d4c3", volumeName, "volumeName not correct")
	assert.NoError(t, err, "error is not nil")
}

func TestParseVolumeNameError(t *testing.T) {
	tests := []struct {
		description string
		input       string
	}{
		{
			"no capacity pool value",
			"/storage/netapp/share/b2525764-c2ed-4e9d-a94b-0c0795c0f275",
		},
		{
			"no volume key",
			"/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/b2525764-c2ed-4e9d-a94b-0c0795c0f275",
		},
		{
			"no volume value",
			"/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share",
		},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("Parse volume ID error: %s", test.description), func(t *testing.T) {
			_, _, err := ParseVolumeName(test.input)
			assert.Error(t, err, test.description)
		})
	}
}

func TestIsOVHNotFoundError_Nil(t *testing.T) {
	result := IsOVHNotFoundError(nil)

	assert.False(t, result, "result should be false")
}

func TestIsOVHNotFoundError_NotFound(t *testing.T) {
	err := &ovh.APIError{
		Code:    http.StatusNotFound,
		Message: "Resource not found",
		Class:   "Client::NotFound",
		QueryID: "EU.ext-99.foobar",
	}

	result := IsOVHNotFoundError(err)

	assert.True(t, result, "Expected error to be a not found error")
}

func TestIsOVHNotFoundError_OtherHTTPError(t *testing.T) {
	err := &ovh.APIError{
		Code: http.StatusBadRequest,
	}

	result := IsOVHNotFoundError(err)

	assert.False(t, result, "result should be false")
}

func TestIsOVHNotFoundError_OtherError(t *testing.T) {
	err := errors.New("failed")

	result := IsOVHNotFoundError(err)

	assert.False(t, result, "result should be false")
}

func TestMakeURL(t *testing.T) {
	resourcePath := "/share"
	d := getOVHClient()
	assert.Equal(t, fmt.Sprintf("/storage/netapp/%s%s", MockServiceID, resourcePath),
		d.makeURL(MockServiceID, resourcePath), "Wrong URL is returned")
}

func TestNewVolumeFromEFSVolume(t *testing.T) {
	d := getOVHClient()

	efsVol := &EFSVolume{
		ID:              "/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share/b2525764-c2ed-4e9d-a94b-0c0795c0f275",
		Name:            "my-volume",
		Protocol:        "NFS",
		SizeInGigabytes: 100,
		Status:          "available",
	}

	actual, err := d.newVolumeFromEFSVolume(ctx, efsVol)

	expected := &Volume{
		ID:              "/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share/b2525764-c2ed-4e9d-a94b-0c0795c0f275",
		ServiceID:       "899879fb-73ad-48fe-b3cc-a963a27e1050",
		Name:            "my-volume",
		Protocol:        "NFS",
		SizeInGigabytes: 100,
		Status:          "available",
	}

	assert.Equal(t, expected, actual, "volume is not equal")
	assert.NoError(t, err, "no error is expected")
}

func mockUnAuthorizedResponseError() {
	httpmock.RegisterResponder(http.MethodGet, "https://eu.api.ovh.com/1.0/auth/time",
		httpmock.NewStringResponder(http.StatusUnauthorized, ""))
}

func mockGetOAuth2TokenResponse() {
	output := `{"access_token": "cccccccccccccccc", "token_type": "Bearer", "expires_in": 11, "scope":"all"}`
	httpmock.RegisterResponder(http.MethodPost, "https://www.ovh.com/auth/oauth2/token",
		httpmock.NewStringResponder(http.StatusOK, output))
}

func mockVolumesResponse() {
	mockGetOAuth2TokenResponse()

	vols := []*Volume{
		{
			CreatedAt:       time.Now(),
			ID:              "049efea3-c571-4099-b101-79d11686db1b",
			Name:            "my-volume",
			Protocol:        ProtocolTypeNFS,
			SizeInGigabytes: 10,
			Status:          VolumeStatusAvailable,
		},
	}
	resp, _ := json.Marshal(vols)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share", MockServiceID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockVolumesEmptyResponse() {
	mockGetOAuth2TokenResponse()

	vols := []*Volume{}
	resp, _ := json.Marshal(vols)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share", MockServiceID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func TestClient_Volumes(t *testing.T) {
	tests := []struct {
		mockFunction    func()
		isErrorExpected bool
		isResultEmpty   bool
	}{
		{mockFunction: mockVolumesResponse, isErrorExpected: false, isResultEmpty: false},
		{mockFunction: mockUnAuthorizedResponseError, isErrorExpected: true, isResultEmpty: true},
		{mockFunction: mockVolumesEmptyResponse, isErrorExpected: false, isResultEmpty: true},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("Volumes %d", i), func(t *testing.T) {
			httpmock.Activate()
			d := getOVHClient()
			entry.mockFunction()
			volumes, err := d.Volumes(ctx)

			if entry.isErrorExpected {
				assert.Error(t, err, "An error is expected.")
			} else {
				assert.NoError(t, err, "Volumes retrieval failed.")
				if !entry.isResultEmpty {
					assert.NotNil(t, volumes, "Volumes should be returned.")
				}
			}

			httpmock.DeactivateAndReset()
		})
	}
}

func mockVolumeByCreationTokenResponse() {
	mockGetOAuth2TokenResponse()

	vols := []*Volume{
		{
			CreatedAt:       time.Now(),
			ID:              "9f14094d-56b9-4aed-bd2b-4e1c14640c89",
			Protocol:        ProtocolTypeNFS,
			Name:            "newVolume",
			SizeInGigabytes: 100,
			Status:          VolumeStatusAvailable,
			CreationToken:   "pvc-8e3a6d6b-018e-4243-92b7-292bd903c639",
		},
	}
	resp, _ := json.Marshal(vols)
	httpmock.RegisterResponder(http.MethodGet,
		fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share?detail=true&mountPointName=%s", MockServiceID,
			"pvc-8e3a6d6b-018e-4243-92b7-292bd903c639"),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockVolumeByCreationTokenEmptyVolumesResponse() {
	mockGetOAuth2TokenResponse()

	vols := []*Volume{}
	resp, _ := json.Marshal(vols)
	httpmock.RegisterResponder(http.MethodGet,
		fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share?mountPointName=%s", MockServiceID,
			"pvc-8e3a6d6b-018e-4243-92b7-292bd903c639"),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockVolumeByCreationTokenMultipleVolumesResponse() {
	mockGetOAuth2TokenResponse()
	vols := []*Volume{
		{
			CreatedAt: time.Now(),
			ID:        "9f14094d-56b9-4aed-bd2b-4e1c14640c89",
			Protocol:  ProtocolTypeNFS,

			Name:            "newVolume",
			SizeInGigabytes: 100,
			Status:          VolumeStatusAvailable,
			CreationToken:   "pvc-8e3a6d6b-018e-4243-92b7-292bd903c639",
		},
		{
			CreatedAt:       time.Now(),
			ID:              "9f14094d-56b9-4aed-bd2b-4e1c14640c90",
			Protocol:        ProtocolTypeNFS,
			Name:            "newVolume",
			SizeInGigabytes: 100,
			Status:          VolumeStatusAvailable,
			CreationToken:   "pvc-8e3a6d6b-018e-4243-92b7-292bd903c639",
		},
	}
	resp, _ := json.Marshal(vols)
	httpmock.RegisterResponder(http.MethodGet,
		fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share?mountPointName=%s", MockServiceID,
			"pvc-8e3a6d6b-018e-4243-92b7-292bd903c639"),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func TestClient_VolumeByMountPointName(t *testing.T) {
	tests := []struct {
		mockFunction    func()
		mountPointName  string
		isErrorExpected bool
	}{
		{
			mockFunction:    mockVolumeByCreationTokenResponse,
			mountPointName:  "pvc-8e3a6d6b-018e-4243-92b7-292bd903c639",
			isErrorExpected: false,
		},
		{
			mockFunction:    mockVolumeByCreationTokenMultipleVolumesResponse,
			mountPointName:  "pvc-8e3a6d6b-018e-4243-92b7-292bd903c639",
			isErrorExpected: true,
		},
		{
			mockFunction:    mockVolumeByCreationTokenEmptyVolumesResponse,
			mountPointName:  "pvc-8e3a6d6b-018e-4243-92b7-292bd903c639",
			isErrorExpected: true,
		},
		{
			mockFunction:    mockUnAuthorizedResponseError,
			mountPointName:  "pvc-8e3a6d6b-018e-4243-92b7-292bd903c639",
			isErrorExpected: true,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("VolumeByCreationToken %d", i), func(t *testing.T) {
			httpmock.Activate()
			d := getOVHClient()
			entry.mockFunction()
			volume, err := d.VolumeByMountPointName(ctx, entry.mountPointName)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected.")
			} else {
				assert.NoError(t, err, "no error is expected")
				assert.NotNil(t, volume, "Volume should be returned")
			}

			httpmock.DeactivateAndReset()
		})
	}
}

func TestClient_VolumeExistsByMountPointName(t *testing.T) {
	tests := []struct {
		mockFunction    func()
		mountPointName  string
		isErrorExpected bool
		checkForVolume  bool
	}{
		{
			mockFunction:    mockVolumeByCreationTokenResponse,
			mountPointName:  "pvc-8e3a6d6b-018e-4243-92b7-292bd903c639",
			isErrorExpected: false,
			checkForVolume:  true,
		},
		{
			mockFunction:    mockVolumeByCreationTokenMultipleVolumesResponse,
			mountPointName:  "pvc-8e3a6d6b-018e-4243-92b7-292bd903c639",
			isErrorExpected: false,
			checkForVolume:  false,
		},
		{
			mockFunction:    mockVolumeByCreationTokenEmptyVolumesResponse,
			mountPointName:  "pvc-8e3a6d6b-018e-4243-92b7-292bd903c639",
			isErrorExpected: false,
		},
		{
			mockFunction:    mockUnAuthorizedResponseError,
			mountPointName:  "pvc-8e3a6d6b-018e-4243-92b7-292bd903c639",
			isErrorExpected: false,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("VolumeExistsByCreationToken %d", i), func(t *testing.T) {
			httpmock.Activate()
			d := getOVHClient()
			entry.mockFunction()
			exists, volume, err := d.VolumeExistsByMountPointName(ctx, entry.mountPointName)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
				assert.False(t, exists, "volume should not exist")
			} else {
				assert.NoError(t, err, "no error is expected")
				if entry.checkForVolume {
					assert.True(t, exists, "volume should exist")
					assert.NotNil(t, volume, "volume should be returned")
				}
			}

			httpmock.DeactivateAndReset()
		})
	}
}

func mockVolumeByIDResponse() {
	mockGetOAuth2TokenResponse()

	vol := Volume{
		CreatedAt:       time.Now(),
		ID:              "9f14094d-56b9-4aed-bd2b-4e1c14640c89",
		Protocol:        ProtocolTypeNFS,
		Name:            "newVolume",
		SizeInGigabytes: 100,
		Status:          VolumeStatusAvailable,
	}
	resp, _ := json.Marshal(vol)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/9f14094d-56b9-4aed-bd2b-4e1c14640c89", MockServiceID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockVolumeByIDNotFoundResponse() {
	mockGetOAuth2TokenResponse()

	err := &ovh.APIError{
		Code:    http.StatusNotFound,
		Message: "Resource not found",
		Class:   "Client::NotFound",
		QueryID: "EU.ext-99.foobar",
	}
	errJSON, _ := json.Marshal(err)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s", MockServiceID, MockVolumeID),
		httpmock.NewBytesResponder(http.StatusNotFound, errJSON))
}

func TestClient_VolumeByID(t *testing.T) {
	volumeID := fmt.Sprintf("/storage/netapp/%s/share/%s", MockServiceID, MockVolumeID)

	tests := []struct {
		mockFunction    func()
		volumeID        string
		isErrorExpected bool
	}{
		{mockFunction: mockVolumeByIDResponse, volumeID: volumeID, isErrorExpected: false},
		{mockFunction: mockUnAuthorizedResponseError, isErrorExpected: true},
		{mockFunction: mockVolumeByIDNotFoundResponse, isErrorExpected: true},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("VolumeByID %d", i), func(t *testing.T) {
			httpmock.Activate()
			d := getOVHClient()
			entry.mockFunction()
			_, err := d.VolumeByID(ctx, entry.volumeID)

			if entry.isErrorExpected {
				assert.Error(t, err, "An error is expected.")
			} else {
				assert.NoError(t, err, "No error is expected.")
			}
			httpmock.DeactivateAndReset()
		})
	}
}

func TestClient_VolumeExistsByID(t *testing.T) {
	volumeID := fmt.Sprintf("/storage/netapp/%s/share/%s", MockServiceID, MockVolumeID)

	tests := []struct {
		mockFunction    func()
		volumeID        string
		isErrorExpected bool
		checkForVolume  bool
	}{
		{
			mockFunction:    mockVolumeByIDResponse,
			volumeID:        volumeID,
			isErrorExpected: false,
			checkForVolume:  true,
		},
		{
			mockFunction:    mockUnAuthorizedResponseError,
			volumeID:        volumeID,
			isErrorExpected: true,
		},
		{
			mockFunction:    mockVolumeByIDNotFoundResponse,
			volumeID:        volumeID,
			isErrorExpected: false,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("VolumeExistsByID %d", i), func(t *testing.T) {
			httpmock.Activate()
			d := getOVHClient()
			entry.mockFunction()
			exists, volume, err := d.VolumeExistsByID(ctx, entry.volumeID)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
				assert.False(t, exists, "volume should not exist")
			} else {
				assert.NoError(t, err, "no error is expected")
				if entry.checkForVolume {
					assert.True(t, exists, "volume should exist")
					assert.NotNil(t, volume, "volume should be returned")
				}
			}

			httpmock.DeactivateAndReset()
		})
	}
}

func mockVolumeByIDResponseCreatingState() {
	mockGetOAuth2TokenResponse()

	vol := Volume{
		CreatedAt:       time.Now(),
		ID:              MockVolumeID,
		Protocol:        ProtocolTypeNFS,
		Name:            "newVolume",
		SizeInGigabytes: 10,
		Status:          VolumeStatusCreating,
	}
	resp, _ := json.Marshal(vol)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/9f14094d-56b9-4aed-bd2b-4e1c14640c89", MockServiceID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockVolumeByIDResponseErrorState() {
	mockGetOAuth2TokenResponse()

	vol := Volume{
		CreatedAt:       time.Now(),
		ID:              MockVolumeID,
		Protocol:        ProtocolTypeNFS,
		Name:            "newVolume",
		SizeInGigabytes: 10,
		Status:          VolumeStatusError,
	}
	resp, _ := json.Marshal(vol)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/9f14094d-56b9-4aed-bd2b-4e1c14640c89", MockServiceID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockVolumeByIDResponseNoState() {
	mockGetOAuth2TokenResponse()

	vol := Volume{
		CreatedAt:       time.Now(),
		ID:              "9f14094d-56b9-4aed-bd2b-4e1c14640c89",
		Protocol:        ProtocolTypeNFS,
		Name:            "newVolume",
		SizeInGigabytes: 10,
		Status:          "",
	}
	resp, _ := json.Marshal(vol)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/9f14094d-56b9-4aed-bd2b-4e1c14640c89", MockServiceID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func TestClient_WaitForVolumeStatus(t *testing.T) {
	tests := []struct {
		mockFunction    func()
		state           string
		desiredState    string
		isErrorExpected bool
	}{
		{
			mockFunction:    mockVolumeByIDResponse,
			state:           VolumeStatusCreating,
			desiredState:    VolumeStatusAvailable,
			isErrorExpected: false,
		},
		{
			mockFunction:    mockVolumeByIDResponseCreatingState,
			state:           VolumeStatusCreating,
			desiredState:    VolumeStatusAvailable,
			isErrorExpected: true,
		},
		{
			mockFunction:    mockVolumeByIDResponseErrorState,
			state:           VolumeStatusCreating,
			desiredState:    VolumeStatusAvailable,
			isErrorExpected: true,
		},
		{
			mockFunction:    mockVolumeByIDResponseNoState,
			state:           VolumeStatusCreating,
			desiredState:    VolumeStatusAvailable,
			isErrorExpected: true,
		},
		{
			mockFunction:    mockVolumeByIDNotFoundResponse,
			state:           VolumeStatusDeleting,
			desiredState:    VolumeStatusDeleted,
			isErrorExpected: false,
		},
		{
			mockFunction:    mockUnAuthorizedResponseError,
			state:           VolumeStatusCreating,
			desiredState:    VolumeStatusAvailable,
			isErrorExpected: true,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("WaitForVolumeStatus %d", i), func(t *testing.T) {
			httpmock.Activate()
			d := getOVHClient()
			entry.mockFunction()

			volume := Volume{ID: CreateVolumeID(MockServiceID, MockVolumeID), Status: entry.state}
			_, err := d.WaitForVolumeStatus(ctx, &volume, entry.desiredState, []string{"error"}, 1*time.Second)

			if entry.isErrorExpected {
				assert.Error(t, err, "An error is expected.")
			} else {
				assert.NoError(t, err, "No error is expected.")
			}
			httpmock.DeactivateAndReset()
		})
	}
}

func mockCreateVolumeResponse() {
	mockGetOAuth2TokenResponse()

	vol := Volume{
		ID:              "9f14094d-56b9-4aed-bd2b-4e1c14640c89",
		Name:            "newVolume",
		SizeInGigabytes: 10,
		Status:          VolumeStatusCreating,
	}
	resp, _ := json.Marshal(vol)
	httpmock.RegisterMatcherResponder(
		http.MethodPost,
		fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share", MockServiceID),
		tdhttpmock.JSONBody(VolumeCreateRequest{
			Name:            "pvc-07d29f37-0f34-40d4-a74e-a2e475a0e934",
			Protocol:        "NFS",
			SizeInGigabytes: 10,
		}),
		httpmock.NewBytesResponder(http.StatusCreated, resp))
}

func TestClient_CreateVolume(t *testing.T) {
	tests := []struct {
		mockFunction    func()
		volumeRequest   VolumeCreateRequest
		isErrorExpected bool
	}{
		{
			mockFunction:    mockCreateVolumeResponse,
			volumeRequest:   VolumeCreateRequest{ServiceID: MockServiceID, Name: "pvc-07d29f37-0f34-40d4-a74e-a2e475a0e934", Protocol: "NFS", SizeInGigabytes: 10},
			isErrorExpected: false,
		},
		{
			mockFunction:    mockUnAuthorizedResponseError,
			volumeRequest:   VolumeCreateRequest{ServiceID: MockServiceID, Name: "pvc-07d29f37-0f34-40d4-a74e-a2e475a0e934", Protocol: "NFS", SizeInGigabytes: 10},
			isErrorExpected: true,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("CreateVolume %d", i), func(t *testing.T) {
			httpmock.Activate()
			d := getOVHClient()
			entry.mockFunction()
			_, err := d.CreateVolume(ctx, &entry.volumeRequest)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
			} else {
				assert.NoError(t, err, "volume creation failed")
			}

			httpmock.DeactivateAndReset()
		})
	}
}

func mockExtendVolumeResponse() {
	mockGetOAuth2TokenResponse()

	httpmock.RegisterMatcherResponder(http.MethodPost,
		fmt.Sprintf("%s/%s/share/9f14094d-56b9-4aed-bd2b-4e1c14640c89/extend", MockAPIURL, MockServiceID),
		tdhttpmock.JSONBody(VolumeResizeRequest{
			SizeInGigabytes: 200,
		}),
		httpmock.NewBytesResponder(http.StatusAccepted, nil))
}

func TestClient_ResizeVolume(t *testing.T) {
	tests := []struct {
		mockFunction    func()
		volumeSize      int64
		newVolumeSize   int64
		isErrorExpected bool
	}{
		{mockFunction: mockExtendVolumeResponse, volumeSize: 100, newVolumeSize: 200, isErrorExpected: false},
		{mockFunction: mockUnAuthorizedResponseError, volumeSize: 100, newVolumeSize: 99, isErrorExpected: true},
		{mockFunction: nil, volumeSize: 100, newVolumeSize: 100, isErrorExpected: false},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("ResizeVolume %d", i), func(t *testing.T) {
			httpmock.Activate()
			c := getOVHClient()
			if entry.mockFunction != nil {
				entry.mockFunction()
			}

			volumeID := CreateVolumeID(MockServiceID, MockVolumeID)
			volume := Volume{ServiceID: MockServiceID, ID: volumeID, Status: VolumeStatusAvailable, SizeInGigabytes: entry.volumeSize}
			err := c.ResizeVolume(ctx, &volume, entry.newVolumeSize)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
			} else {
				assert.NoError(t, err, "no error is expected")
			}

			httpmock.DeactivateAndReset()
		})
	}
}

func mockDeleteVolumeResponse() {
	mockGetOAuth2TokenResponse()

	httpmock.RegisterResponder(http.MethodDelete, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s", MockServiceID, MockVolumeID),
		httpmock.NewBytesResponder(http.StatusOK, nil))
}

func mockDeleteVolumeErrorResponse() {
	mockGetOAuth2TokenResponse()

	httpmock.RegisterResponder(http.MethodDelete, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s", MockServiceID, MockVolumeID),
		httpmock.NewBytesResponder(http.StatusBadRequest, nil))
}

func mockDeleteVolumeNotFoundResponse() {
	mockGetOAuth2TokenResponse()

	err := &ovh.APIError{
		Code:    http.StatusNotFound,
		Message: "Resource not found",
		Class:   "Client::NotFound",
		QueryID: "EU.ext-99.foobar",
	}
	errJSON, _ := json.Marshal(err)
	httpmock.RegisterResponder(http.MethodDelete, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s", MockServiceID, MockVolumeID),
		httpmock.NewBytesResponder(http.StatusNotFound, errJSON))
}

func TestClient_DeleteVolume(t *testing.T) {
	tests := []struct {
		mockFunction    func()
		isErrorExpected bool
	}{
		{mockFunction: mockDeleteVolumeResponse, isErrorExpected: false},
		{mockFunction: mockDeleteVolumeNotFoundResponse, isErrorExpected: false},
		{mockFunction: mockDeleteVolumeErrorResponse, isErrorExpected: true},
		{mockFunction: mockUnAuthorizedResponseError, isErrorExpected: true},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("DeleteVolume %d", i), func(t *testing.T) {
			httpmock.Activate()
			d := getOVHClient()
			entry.mockFunction()

			volumeID := CreateVolumeID(MockServiceID, MockVolumeID)
			err := d.DeleteVolume(ctx, &Volume{ID: volumeID, ServiceID: MockServiceID})

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
			} else {
				assert.NoError(t, err, "no error was expected")
			}

			httpmock.DeactivateAndReset()
		})
	}
}

func mockAccessPathsResponse() {
	mockGetOAuth2TokenResponse()

	accessPaths := []*AccessPath{
		{
			Path: "10.201.132.1:/share_cc16b532_9bf1_4c8f_91ad_3b59725f9e4a",
		},
	}
	resp, _ := json.Marshal(accessPaths)
	httpmock.RegisterResponder(
		http.MethodGet,
		fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/accessPath", MockServiceID, MockVolumeID),
		httpmock.NewBytesResponder(http.StatusCreated, resp))
}

func mockAccessPathsErrorResponse() {
	mockGetOAuth2TokenResponse()

	httpmock.RegisterResponder(
		http.MethodGet,
		fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/accessPath", MockServiceID, MockVolumeID),
		httpmock.NewBytesResponder(http.StatusInternalServerError, nil))
}

func TestClient_VolumeAccessPaths(t *testing.T) {
	volumeID := CreateVolumeID(MockServiceID, MockVolumeID)

	tests := []struct {
		mockFunction    func()
		volume          *Volume
		isErrorExpected bool
	}{
		{
			mockFunction:    mockAccessPathsResponse,
			volume:          &Volume{ID: volumeID, ServiceID: MockServiceID},
			isErrorExpected: false,
		},
		{
			mockFunction:    mockAccessPathsResponse,
			volume:          &Volume{ID: MockVolumeID, ServiceID: MockServiceID},
			isErrorExpected: true,
		},
		{
			mockFunction:    mockAccessPathsErrorResponse,
			volume:          &Volume{ID: volumeID, ServiceID: MockServiceID},
			isErrorExpected: true,
		},
		{
			mockFunction:    mockUnAuthorizedResponseError,
			volume:          &Volume{ID: volumeID, ServiceID: MockServiceID},
			isErrorExpected: true,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("VolumeAccessPaths %d", i), func(t *testing.T) {
			d := getOVHClient()

			httpmock.Activate()
			entry.mockFunction()
			aps, err := d.VolumeAccessPaths(ctx, entry.volume)

			if entry.isErrorExpected {
				assert.Error(t, err, "An error is expected.")
			} else {
				assert.NoError(t, err, "Volume creation failed.")
				assert.Equal(t, []*AccessPath{{Path: "10.201.132.1:/share_cc16b532_9bf1_4c8f_91ad_3b59725f9e4a"}}, aps)
			}
			httpmock.DeactivateAndReset()
		})
	}
}

func mockExportRulesForVolumeResponse() {
	mockGetOAuth2TokenResponse()

	exportRules := []*ExportRule{
		{
			ID:          "",
			Status:      ExportRuleStatusActive,
			AccessLevel: "rw",
			AccessTo:    "10.0.0.1",
		},
		{
			ID:          "",
			Status:      ExportRuleStatusActive,
			AccessLevel: "rw",
			AccessTo:    "10.0.0.2",
		},
	}
	resp, _ := json.Marshal(exportRules)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl", MockServiceID, MockVolumeID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockExportRulesForVolumeCIDRResponse() {
	mockGetOAuth2TokenResponse()

	exportRules := []*ExportRule{
		{
			ID:          "",
			Status:      ExportRuleStatusActive,
			AccessLevel: "rw",
			AccessTo:    "10.0.0.1/32",
		},
		{
			ID:          "",
			Status:      ExportRuleStatusActive,
			AccessLevel: "rw",
			AccessTo:    "10.5.0.0/24",
		},
	}
	resp, _ := json.Marshal(exportRules)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl", MockServiceID, MockVolumeID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockExportRulesForVolumeErrorResponse() {
	mockGetOAuth2TokenResponse()

	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl", MockServiceID, MockVolumeID),
		httpmock.NewBytesResponder(http.StatusInternalServerError, nil))
}

func mockExportRulesForVolumeEmptyResponse() {
	mockGetOAuth2TokenResponse()

	exportRules := []*ExportRule{}
	resp, _ := json.Marshal(exportRules)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl", MockServiceID, MockVolumeID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func TestClient_ExportRulesForVolume(t *testing.T) {
	volumeID := fmt.Sprintf("/storage/netapp/%s/share/%s", MockServiceID, MockVolumeID)

	tests := []struct {
		mockFunction    func()
		volume          *Volume
		isErrorExpected bool
	}{
		{
			mockFunction:    mockExportRulesForVolumeResponse,
			volume:          &Volume{ID: volumeID, ServiceID: MockServiceID},
			isErrorExpected: false,
		},
		{
			mockFunction:    mockExportRulesForVolumeResponse,
			volume:          &Volume{ID: "", ServiceID: MockServiceID},
			isErrorExpected: true,
		},
		{
			mockFunction:    mockExportRulesForVolumeErrorResponse,
			volume:          &Volume{ID: volumeID, ServiceID: MockServiceID},
			isErrorExpected: true,
		},
		{
			mockFunction:    mockExportRulesForVolumeEmptyResponse,
			volume:          &Volume{ID: volumeID, ServiceID: MockServiceID},
			isErrorExpected: false,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("ExportRules %d", i), func(t *testing.T) {
			d := getOVHClient()

			httpmock.Activate()
			entry.mockFunction()

			result, err := d.ExportRulesForVolume(ctx, entry.volume)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
				assert.Nil(t, result, "expected result to be nil")
			} else {
				assert.NoError(t, err, "no error is expected")
				assert.NotNil(t, result, "expected result not to be nil")
			}

			httpmock.Deactivate()
		})
	}
}

func mockExportRuleByIDResponse() {
	mockGetOAuth2TokenResponse()

	exportRule := &ExportRule{
		ID:          MockExportRuleID,
		Status:      ExportRuleStatusActive,
		AccessLevel: "rw",
		AccessTo:    "10.0.0.1/32",
	}
	resp, _ := json.Marshal(exportRule)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl/%s", MockServiceID, MockVolumeID, MockExportRuleID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockExportRuleByIDErrorResponse() {
	mockGetOAuth2TokenResponse()

	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl/%s", MockServiceID, MockVolumeID, MockExportRuleID),
		httpmock.NewBytesResponder(http.StatusInternalServerError, nil))
}

func mockExportRuleByIDNotFoundResponse() {
	mockGetOAuth2TokenResponse()

	err := &ovh.APIError{
		Code:    http.StatusNotFound,
		Message: "Resource not found",
		Class:   "Client::NotFound",
		QueryID: "EU.ext-99.foobar",
	}
	errJSON, _ := json.Marshal(err)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl/%s", MockServiceID, MockVolumeID, MockExportRuleID),
		httpmock.NewBytesResponder(http.StatusNotFound, errJSON))
}

func TestClient_ExportRuleByID(t *testing.T) {
	volumeID := fmt.Sprintf("/storage/netapp/%s/share/%s", MockServiceID, MockVolumeID)

	tests := []struct {
		mockFunction    func()
		volume          *Volume
		ruleID          string
		isErrorExpected bool
	}{
		{
			mockFunction:    mockExportRuleByIDResponse,
			volume:          &Volume{ID: volumeID, ServiceID: MockServiceID},
			ruleID:          MockExportRuleID,
			isErrorExpected: false,
		},
		{
			mockFunction:    mockExportRuleByIDResponse,
			volume:          &Volume{ID: "", ServiceID: MockServiceID},
			ruleID:          MockExportRuleID,
			isErrorExpected: true,
		},
		{
			mockFunction:    mockExportRuleByIDErrorResponse,
			volume:          &Volume{ID: volumeID, ServiceID: MockServiceID},
			ruleID:          MockExportRuleID,
			isErrorExpected: true,
		},
		{
			mockFunction:    mockExportRuleByIDNotFoundResponse,
			volume:          &Volume{ID: volumeID, ServiceID: MockServiceID},
			ruleID:          MockExportRuleID,
			isErrorExpected: true,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("ExportRuleByID %d", i), func(t *testing.T) {
			d := getOVHClient()

			httpmock.Activate()
			entry.mockFunction()

			result, err := d.ExportRuleByID(ctx, entry.volume, entry.ruleID)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
				assert.Nil(t, result, "expected result to be nil")
			} else {
				assert.NoError(t, err, "no error is expected")
				assert.NotNil(t, result, "expected result not to be nil")
			}

			httpmock.Deactivate()
		})
	}
}

func mockCreateExportRuleResponse() {
	mockGetOAuth2TokenResponse()

	exportRule := &ExportRule{
		ID:          MockExportRuleID,
		Status:      ExportRuleStatusApplying,
		AccessLevel: "rw",
		AccessTo:    "10.0.0.1",
	}
	resp, _ := json.Marshal(exportRule)
	httpmock.RegisterMatcherResponder(
		http.MethodPost,
		fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl", MockServiceID, MockVolumeID),
		tdhttpmock.JSONBody(ExportRuleCreateRequest{
			AccessLevel: "rw",
			AccessTo:    "10.0.0.1",
		}),
		httpmock.NewBytesResponder(http.StatusCreated, resp))
}

func mockCreateExportRuleErrorResponse() {
	mockGetOAuth2TokenResponse()

	httpmock.RegisterMatcherResponder(
		http.MethodPost,
		fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl", MockServiceID, MockVolumeID),
		tdhttpmock.JSONBody(ExportRuleCreateRequest{
			AccessLevel: "rw",
			AccessTo:    "10.0.0.1",
		}),
		httpmock.NewBytesResponder(http.StatusInternalServerError, nil))
}

func TestClient_CreateExportRule(t *testing.T) {
	volumeID := fmt.Sprintf("/storage/netapp/%s/share/%s", MockServiceID, MockVolumeID)

	tests := []struct {
		mockFunction    func()
		volume          *Volume
		request         *ExportRuleCreateRequest
		isErrorExpected bool
	}{
		{
			mockFunction:    mockCreateExportRuleResponse,
			volume:          &Volume{ID: volumeID, ServiceID: MockServiceID},
			request:         &ExportRuleCreateRequest{AccessLevel: "rw", AccessTo: "10.0.0.1"},
			isErrorExpected: false,
		},
		{
			mockFunction:    mockCreateExportRuleResponse,
			volume:          &Volume{ID: "", ServiceID: MockServiceID},
			request:         &ExportRuleCreateRequest{AccessLevel: "rw", AccessTo: "10.0.0.1"},
			isErrorExpected: true,
		},
		{
			mockFunction:    mockCreateExportRuleErrorResponse,
			volume:          &Volume{ID: volumeID, ServiceID: MockServiceID},
			request:         &ExportRuleCreateRequest{AccessLevel: "rw", AccessTo: "10.0.0.1"},
			isErrorExpected: true,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("CreateExportRule %d", i), func(t *testing.T) {
			d := getOVHClient()

			httpmock.Activate()
			entry.mockFunction()

			result, err := d.CreateExportRule(ctx, entry.volume, entry.request)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
				assert.Nil(t, result, "expected result to be nil")
			} else {
				assert.NoError(t, err, "no error is expected")
				assert.NotNil(t, result, "expected result not to be nil")
			}

			httpmock.DeactivateAndReset()
		})
	}
}

func TestClient_ExportRulesExists(t *testing.T) {
	volumeID := fmt.Sprintf("/storage/netapp/%s/share/%s", MockServiceID, MockVolumeID)

	volume := &Volume{
		ID:        volumeID,
		ServiceID: MockServiceID,
	}

	tests := []struct {
		mockFunction     func()
		exportRules      string
		volume           *Volume
		isErrorExpected  bool
		isOutputExpected bool
	}{
		{
			mockFunction:     mockExportRulesForVolumeResponse,
			exportRules:      "10.0.0.1,10.0.0.2",
			volume:           &Volume{ID: volumeID},
			isErrorExpected:  false,
			isOutputExpected: true,
		},
		{
			mockFunction:     mockExportRulesForVolumeCIDRResponse,
			exportRules:      "10.0.0.1/32,10.5.0.0/24",
			volume:           &Volume{ID: volumeID},
			isErrorExpected:  false,
			isOutputExpected: true,
		},
		{
			mockFunction:     mockExportRulesForVolumeResponse,
			exportRules:      "10.10.10.10",
			volume:           &Volume{ID: volumeID},
			isErrorExpected:  false,
			isOutputExpected: false,
		},
		{
			mockFunction:     mockExportRulesForVolumeResponse,
			exportRules:      "",
			volume:           &Volume{ID: volumeID},
			isErrorExpected:  false,
			isOutputExpected: true,
		},
		{
			mockFunction:     mockExportRulesForVolumeEmptyResponse,
			exportRules:      "10.0.0.1,10.0.0.2",
			volume:           &Volume{ID: volumeID},
			isErrorExpected:  false,
			isOutputExpected: false,
		},
		{
			mockFunction:     mockExportRulesForVolumeErrorResponse,
			exportRules:      "10.0.0.1,10.0.0.2",
			volume:           &Volume{ID: volumeID},
			isErrorExpected:  true,
			isOutputExpected: false,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("ExportRulesExists %d", i), func(t *testing.T) {
			d := getOVHClient()

			httpmock.Activate()
			entry.mockFunction()

			exists, exportRules, err := d.ExportRulesExists(ctx, volume, entry.exportRules)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
				assert.False(t, exists, "export rules should not exist")
			} else {
				assert.NoError(t, err, "no error is expected")
				if entry.isOutputExpected {
					assert.True(t, exists, "export rules should exist")
					assert.NotNil(t, exportRules, "export rules should be returned")
				} else {
					assert.False(t, exists, "export rules should not exist")
					assert.Nil(t, exportRules, "export rules should not be returned")
				}
			}

			httpmock.Deactivate()
		})
	}
}

func mockExportRuleByIDQueuedToApplyStateResponse() {
	mockGetOAuth2TokenResponse()

	exportRule := &ExportRule{
		ID:          MockExportRuleID,
		Status:      ExportRuleStatusQueuedToApply,
		AccessLevel: "rw",
		AccessTo:    "10.0.0.1/32",
	}
	resp, _ := json.Marshal(exportRule)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl/%s", MockServiceID, MockVolumeID, MockExportRuleID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockExportRuleByIDApplyingStateResponse() {
	mockGetOAuth2TokenResponse()

	exportRule := &ExportRule{
		ID:          MockExportRuleID,
		Status:      ExportRuleStatusApplying,
		AccessLevel: "rw",
		AccessTo:    "10.0.0.1/32",
	}
	resp, _ := json.Marshal(exportRule)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl/%s", MockServiceID, MockVolumeID, MockExportRuleID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockExportRuleByIDErrorStateResponse() {
	mockGetOAuth2TokenResponse()

	exportRule := &ExportRule{
		ID:          MockExportRuleID,
		Status:      ExportRuleStatusError,
		AccessLevel: "rw",
		AccessTo:    "10.0.0.1/32",
	}
	resp, _ := json.Marshal(exportRule)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl/%s", MockServiceID, MockVolumeID, MockExportRuleID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockExportRuleByIDNoStateResponse() {
	mockGetOAuth2TokenResponse()

	exportRule := &ExportRule{
		ID:          MockExportRuleID,
		Status:      "",
		AccessLevel: "rw",
		AccessTo:    "10.0.0.1/32",
	}
	resp, _ := json.Marshal(exportRule)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl/%s", MockServiceID, MockVolumeID, MockExportRuleID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func TestClient_WaitForExportRuleStatus(t *testing.T) {
	tests := []struct {
		mockFunction    func()
		state           string
		desiredState    string
		isErrorExpected bool
	}{
		{
			mockFunction:    mockExportRuleByIDResponse,
			state:           ExportRuleStatusQueuedToApply,
			desiredState:    ExportRuleStatusActive,
			isErrorExpected: false,
		},
		{
			mockFunction:    mockExportRuleByIDQueuedToApplyStateResponse,
			state:           ExportRuleStatusQueuedToApply,
			desiredState:    ExportRuleStatusActive,
			isErrorExpected: true,
		},
		{
			mockFunction:    mockExportRuleByIDApplyingStateResponse,
			state:           ExportRuleStatusQueuedToApply,
			desiredState:    ExportRuleStatusActive,
			isErrorExpected: true,
		},
		{
			mockFunction:    mockExportRuleByIDErrorStateResponse,
			state:           ExportRuleStatusQueuedToApply,
			desiredState:    ExportRuleStatusActive,
			isErrorExpected: true,
		},
		{
			mockFunction:    mockExportRuleByIDNoStateResponse,
			state:           ExportRuleStatusQueuedToApply,
			desiredState:    ExportRuleStatusActive,
			isErrorExpected: true,
		},
		{
			mockFunction:    mockExportRuleByIDNotFoundResponse,
			state:           ExportRuleStatusDenying,
			desiredState:    ExportRuleStatusDenied,
			isErrorExpected: false,
		},
		{
			mockFunction:    mockUnAuthorizedResponseError,
			state:           ExportRuleStatusQueuedToApply,
			desiredState:    ExportRuleStatusActive,
			isErrorExpected: true,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("WaitForExportRuleStatus %d", i), func(t *testing.T) {
			d := getOVHClient()

			httpmock.Activate()
			entry.mockFunction()

			volume := Volume{ID: CreateVolumeID(MockServiceID, MockVolumeID), ServiceID: MockServiceID}
			exportRule := ExportRule{ID: MockExportRuleID, Status: entry.state}

			_, err := d.WaitForExportRuleStatus(ctx, &volume, &exportRule, entry.desiredState, []string{ExportRuleStatusError}, 1*time.Second)

			if entry.isErrorExpected {
				assert.Error(t, err, "An error is expected.")
			} else {
				assert.NoError(t, err, "No error is expected.")
			}
			httpmock.DeactivateAndReset()
		})
	}
}

func mockDeleteExportRuleResponse() {
	mockGetOAuth2TokenResponse()

	httpmock.RegisterResponder(http.MethodDelete, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl/%s", MockServiceID, MockVolumeID, MockExportRuleID),
		httpmock.NewBytesResponder(http.StatusAccepted, nil))
}

func mockDeleteExportRuleErrorResponse() {
	mockGetOAuth2TokenResponse()

	httpmock.RegisterResponder(http.MethodDelete, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/acl/%s", MockServiceID, MockVolumeID, MockExportRuleID),
		httpmock.NewBytesResponder(http.StatusInternalServerError, nil))
}

func TestClient_DeleteExportRule(t *testing.T) {
	volumeID := fmt.Sprintf("/storage/netapp/%s/share/%s", MockServiceID, MockVolumeID)
	volume := &Volume{ID: volumeID, ServiceID: MockServiceID}
	exportRule := &ExportRule{ID: MockExportRuleID}

	tests := []struct {
		mockFunction    func()
		volume          *Volume
		exportRule      *ExportRule
		isErrorExpected bool
	}{
		{
			mockFunction:    mockDeleteExportRuleResponse,
			volume:          volume,
			exportRule:      exportRule,
			isErrorExpected: false,
		},
		{
			mockFunction:    mockDeleteExportRuleResponse,
			volume:          &Volume{ID: MockVolumeID, ServiceID: MockServiceID},
			exportRule:      exportRule,
			isErrorExpected: true,
		},
		{
			mockFunction:    mockDeleteExportRuleErrorResponse,
			volume:          volume,
			exportRule:      exportRule,
			isErrorExpected: true,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("DeleteExportRule %d", i), func(t *testing.T) {
			d := getOVHClient()

			httpmock.Activate()
			entry.mockFunction()

			err := d.DeleteExportRule(ctx, entry.volume, entry.exportRule)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
			} else {
				assert.NoError(t, err, "no error is expected")
			}

			httpmock.DeactivateAndReset()
		})
	}
}

func TestCreateSnapshotID(t *testing.T) {
	actual := CreateSnapshotID("899879fb-73ad-48fe-b3cc-a963a27e1050", "b2525764-c2ed-4e9d-a94b-0c0795c0f275",
		"b6a8c410-d7bf-4f68-96ca-4a1d392ee7d1")

	expected := "/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share/b2525764-c2ed-4e9d-a94b-0c0795c0f275/snapshot/b6a8c410-d7bf-4f68-96ca-4a1d392ee7d1"

	assert.Equal(t, expected, actual, "snapshot IDs not equal")
}

func TestParseSnapshotID(t *testing.T) {
	capacityPool, volumeID, snapshotID, err := ParseSnapshotID("/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share/b2525764-c2ed-4e9d-a94b-0c0795c0f275/snapshot/b6a8c410-d7bf-4f68-96ca-4a1d392ee7d1")

	assert.Equal(t, "899879fb-73ad-48fe-b3cc-a963a27e1050", capacityPool, "capacity pool not correct")
	assert.Equal(t, "b2525764-c2ed-4e9d-a94b-0c0795c0f275", volumeID, "volume ID not correct")
	assert.Equal(t, "b6a8c410-d7bf-4f68-96ca-4a1d392ee7d1", snapshotID, "snapshot ID not correct")
	assert.NoError(t, err, "error is not nil")
}

func TestParseSnapshotIDError(t *testing.T) {
	tests := []struct {
		description string
		input       string
	}{
		{
			"no capacity pool value",
			"/storage/netapp/share/b2525764-c2ed-4e9d-a94b-0c0795c0f275/snapshot/b6a8c410-d7bf-4f68-96ca-4a1d392ee7d1",
		},

		{
			"no volume key",
			"/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/b2525764-c2ed-4e9d-a94b-0c0795c0f275/snapshot/b6a8c410-d7bf-4f68-96ca-4a1d392ee7d1",
		},
		{
			"no volume value",
			"/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share/snapshot/b6a8c410-d7bf-4f68-96ca-4a1d392ee7d1",
		},
		{
			"no snapshot key",
			"/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share/b2525764-c2ed-4e9d-a94b-0c0795c0f275/b6a8c410-d7bf-4f68-96ca-4a1d392ee7d1",
		},
		{
			"no snapshot value",
			"/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share/b2525764-c2ed-4e9d-a94b-0c0795c0f275/snapshot",
		},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("Parse snapshot ID error: %s", test.description), func(t *testing.T) {
			_, _, _, err := ParseSnapshotID(test.input)
			assert.Error(t, err, "expected error")
		})
	}
}

func TestNewSnapshotFromEFSSnapshot(t *testing.T) {
	d := getOVHClient()

	efsSnap := &EFSSnapshot{
		ID:          "/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share/b2525764-c2ed-4e9d-a94b-0c0795c0f275/snapshot/b6a8c410-d7bf-4f68-96ca-4a1d392ee7d1",
		Name:        "",
		Description: "",
		Path:        ".shapshot/share_snapshot_0c862d6e_dfc1_4f0f_80cf_05e6fbf2426c",
		Status:      "available",
		Type:        "manual",
	}

	actual, err := d.newSnapshotFromEFSSnapshot(ctx, efsSnap)

	expected := &Snapshot{
		ID:        "/storage/netapp/899879fb-73ad-48fe-b3cc-a963a27e1050/share/b2525764-c2ed-4e9d-a94b-0c0795c0f275/snapshot/b6a8c410-d7bf-4f68-96ca-4a1d392ee7d1",
		Name:      "",
		ServiceID: "899879fb-73ad-48fe-b3cc-a963a27e1050",
		Path:      ".shapshot/share_snapshot_0c862d6e_dfc1_4f0f_80cf_05e6fbf2426c",
		Status:    "available",
	}

	assert.Equal(t, expected, actual, "snapshot is not equal")
	assert.NoError(t, err, "no error is expected")
}

func mockSnapshotsForVolumeResponse() {
	mockGetOAuth2TokenResponse()

	snaps := []EFSSnapshot{
		{
			ID:          "ed81bc2c-d4dd-4c9c-8bce-7befc5cefbdd",
			Name:        "",
			Description: "",
			Status:      "available",
			Path:        ".snapshot/share_snapshot_0c862d6e_dfc1_4f0f_80cf_05e6fbf2426c",
			Type:        "manual",
		},
		{
			ID:          "40c2ce45-48b8-4110-9e63-747851f4a818",
			Name:        "weekly.2025-05-25_0015",
			Description: "",
			Status:      "available",
			Path:        ".snapshot/weekly.2025-05-25_0015",
			Type:        "automatic",
		},
		{
			ID:          "7b77bbaf-8999-48ea-ad92-a8478c439d94",
			Name:        "snapshot-441f2851-165c-4abc-b250-4754308add5e",
			Description: "",
			Status:      "available",
			Path:        ".snapshot/share_snapshot_05a17509_2577_4e3a_aeda_f80761c60e57",
			Type:        "manual",
		},
	}
	resp, _ := json.Marshal(snaps)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/snapshot", MockServiceID, MockVolumeID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockSnapshotsForVolumeEmptyResponse() {
	mockGetOAuth2TokenResponse()

	snaps := []EFSSnapshot{}
	resp, _ := json.Marshal(snaps)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/snapshot", MockServiceID, MockVolumeID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func TestClient_SnapshotsForVolume(t *testing.T) {
	volumeID := CreateVolumeID(MockServiceID, MockVolumeID)

	tests := []struct {
		mockFunction    func()
		volume          *Volume
		isErrorExpected bool
		isResultEmpty   bool
	}{
		{mockFunction: mockSnapshotsForVolumeResponse, volume: &Volume{ID: volumeID}, isErrorExpected: false, isResultEmpty: false},
		{mockFunction: mockSnapshotsForVolumeEmptyResponse, volume: &Volume{ID: volumeID}, isErrorExpected: false, isResultEmpty: true},
		{mockFunction: mockUnAuthorizedResponseError, volume: &Volume{ID: volumeID}, isErrorExpected: true},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("SnapshotsForVolume %d", i), func(t *testing.T) {
			d := getOVHClient()
			httpmock.Activate()
			entry.mockFunction()

			snapshots, err := d.SnapshotsForVolume(ctx, entry.volume)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
			} else {
				assert.NoError(t, err, "no error is expected")
				if !entry.isResultEmpty {
					assert.NotNil(t, snapshots, "Snapshots should be returned")
				}
			}

			httpmock.DeactivateAndReset()
		})
	}
}

func TestClient_SnapshotForVolume(t *testing.T) {
	volumeID := CreateVolumeID(MockServiceID, MockVolumeID)

	tests := []struct {
		mockFunction    func()
		volume          *Volume
		isErrorExpected bool
	}{
		{
			mockFunction:    mockSnapshotsForVolumeResponse,
			volume:          &Volume{ID: volumeID},
			isErrorExpected: false,
		},
		{
			mockFunction:    mockSnapshotsForVolumeResponse,
			volume:          &Volume{ID: ""},
			isErrorExpected: true,
		},
		{
			mockFunction:    mockSnapshotsForVolumeEmptyResponse,
			volume:          &Volume{ID: volumeID},
			isErrorExpected: true,
		},
		{
			mockFunction:    mockUnAuthorizedResponseError,
			volume:          &Volume{ID: volumeID},
			isErrorExpected: true,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("SnapshotForVolume %d", i), func(t *testing.T) {
			d := getOVHClient()
			httpmock.Activate()
			entry.mockFunction()

			snapshot, err := d.SnapshotForVolume(ctx, entry.volume, "snapshot-441f2851-165c-4abc-b250-4754308add5e")

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
			} else {
				assert.NoError(t, err, "no error is expected")
				assert.NotNil(t, snapshot, "Snapshot should be returned")
			}

			httpmock.DeactivateAndReset()
		})
	}
}

func mockSnapshotByIDResponse() {
	mockGetOAuth2TokenResponse()

	snap := Snapshot{
		ID:     MockSnapshotID,
		Status: SnapshotStatusAvailable,
	}
	resp, _ := json.Marshal(snap)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/snapshot/%s", MockServiceID, MockVolumeID, MockSnapshotID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockSnapshotByIDResponseCreatingState() {
	mockGetOAuth2TokenResponse()

	snap := Snapshot{
		ID:     MockSnapshotID,
		Status: SnapshotStatusCreating,
	}
	resp, _ := json.Marshal(snap)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/snapshot/%s", MockServiceID, MockVolumeID, MockSnapshotID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockSnapshotByIDResponseErrorState() {
	mockGetOAuth2TokenResponse()

	snap := Snapshot{
		ID:     MockSnapshotID,
		Status: SnapshotStatusError,
	}
	resp, _ := json.Marshal(snap)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/snapshot/%s", MockServiceID, MockVolumeID, MockSnapshotID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockSnapshotByIDResponseNoState() {
	mockGetOAuth2TokenResponse()

	snap := Snapshot{
		ID:     MockSnapshotID,
		Status: "",
	}
	resp, _ := json.Marshal(snap)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/snapshot/%s", MockServiceID, MockVolumeID, MockSnapshotID),
		httpmock.NewBytesResponder(http.StatusOK, resp))
}

func mockSnapshotByIDNotFoundResponse() {
	mockGetOAuth2TokenResponse()

	err := &ovh.APIError{
		Code:    http.StatusNotFound,
		Message: "Resource not found",
		Class:   "Client::NotFound",
		QueryID: "EU.ext-99.foobar",
	}
	errJSON, _ := json.Marshal(err)
	httpmock.RegisterResponder(http.MethodGet, fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/snapshot/%s", MockServiceID, MockVolumeID, MockSnapshotID),
		httpmock.NewBytesResponder(http.StatusNotFound, errJSON))
}

func TestClient_SnapshotByID(t *testing.T) {
	tests := []struct {
		mockFunction    func()
		isErrorExpected bool
	}{
		{mockFunction: mockSnapshotByIDResponse, isErrorExpected: false},
		{mockFunction: mockUnAuthorizedResponseError, isErrorExpected: true},
		{mockFunction: mockSnapshotByIDNotFoundResponse, isErrorExpected: true},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("SnapshotByID %d", i), func(t *testing.T) {
			httpmock.Activate()
			d := getOVHClient()
			entry.mockFunction()

			volume := Volume{ID: CreateVolumeID(MockServiceID, MockVolumeID)}
			snapshotID := CreateSnapshotID(MockServiceID, MockVolumeID, MockSnapshotID)

			snap, err := d.SnapshotByID(ctx, &volume, snapshotID)
			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
			} else {
				assert.NoError(t, err, "no error is expected")
				assert.NotNil(t, snap, "expected snapshot not to be nil")
			}
			httpmock.DeactivateAndReset()
		})
	}
}

func TestClient_WaitForSnapshotStatus(t *testing.T) {
	tests := []struct {
		mockFunction    func()
		state           string
		desiredState    string
		isErrorExpected bool
	}{
		{
			mockFunction:    mockSnapshotByIDResponse,
			state:           SnapshotStatusCreating,
			desiredState:    SnapshotStatusAvailable,
			isErrorExpected: false,
		},
		{
			mockFunction:    mockSnapshotByIDResponseCreatingState,
			state:           SnapshotStatusCreating,
			desiredState:    SnapshotStatusAvailable,
			isErrorExpected: true,
		},
		{
			mockFunction:    mockSnapshotByIDResponseErrorState,
			state:           SnapshotStatusCreating,
			desiredState:    SnapshotStatusAvailable,
			isErrorExpected: true,
		},
		{
			mockFunction:    mockSnapshotByIDResponseNoState,
			state:           SnapshotStatusCreating,
			desiredState:    SnapshotStatusAvailable,
			isErrorExpected: true,
		},
		{
			mockFunction:    mockSnapshotByIDNotFoundResponse,
			state:           SnapshotStatusDeleting,
			desiredState:    SnapshotStatusDeleted,
			isErrorExpected: false,
		},
		{
			mockFunction:    mockUnAuthorizedResponseError,
			state:           SnapshotStatusCreating,
			desiredState:    SnapshotStatusAvailable,
			isErrorExpected: true,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("WaitForSnapshotStatus %d", i), func(t *testing.T) {
			httpmock.Activate()
			d := getOVHClient()
			entry.mockFunction()

			volumeID := CreateVolumeID(MockServiceID, MockVolumeID)
			volume := Volume{ID: volumeID, Status: VolumeStatusAvailable}
			snapshotID := CreateSnapshotID(MockServiceID, MockVolumeID, MockSnapshotID)
			snapshot := Snapshot{ID: snapshotID, Status: entry.state}

			err := d.WaitForSnapshotStatus(ctx, &volume, &snapshot, entry.desiredState, []string{SnapshotStatusError}, 1*time.Second)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected.")
			} else {
				assert.NoError(t, err, "no error is expected.")
			}

			httpmock.DeactivateAndReset()
		})
	}
}

func mockCreateSnapshotResponse() {
	mockGetOAuth2TokenResponse()

	snap := EFSSnapshot{
		ID:          "ed81bc2c-d4dd-4c9c-8bce-7befc5cefbdd",
		Name:        "",
		Description: "",
		Status:      SnapshotStatusCreating,
		Path:        ".snapshot/share_snapshot_0c862d6e_dfc1_4f0f_80cf_05e6fbf2426c",
		Type:        "manual",
	}
	resp, _ := json.Marshal(snap)
	httpmock.RegisterMatcherResponder(
		http.MethodPost,
		fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/snapshot", MockServiceID, MockVolumeID),
		tdhttpmock.JSONBody(SnapshotCreateRequest{
			Name: "snap",
		}),
		httpmock.NewBytesResponder(http.StatusCreated, resp))
}

func TestClient_CreateSnapshot(t *testing.T) {
	tests := []struct {
		mockFunction    func()
		volume          Volume
		snapshotName    string
		isErrorExpected bool
	}{
		{
			mockFunction:    mockCreateSnapshotResponse,
			volume:          Volume{ID: CreateVolumeID(MockServiceID, MockVolumeID)},
			snapshotName:    "snap",
			isErrorExpected: false,
		},
		{
			mockFunction:    mockCreateSnapshotResponse,
			volume:          Volume{ID: MockVolumeID},
			snapshotName:    "snap",
			isErrorExpected: true,
		},
		{
			mockFunction:    mockUnAuthorizedResponseError,
			volume:          Volume{ID: CreateVolumeID(MockServiceID, MockVolumeID)},
			snapshotName:    "snap",
			isErrorExpected: true,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("CreateSnapshot %d", i), func(t *testing.T) {
			httpmock.Activate()
			d := getOVHClient()
			entry.mockFunction()

			snap, err := d.CreateSnapshot(ctx, &entry.volume, entry.snapshotName)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
			} else {
				assert.NoError(t, err, "snapshot creation failed")
				assert.NotNil(t, snap, "a snapshot was expected")
			}

			httpmock.DeactivateAndReset()
		})
	}
}

func mockRestoreSnapshotResponse() {
	mockGetOAuth2TokenResponse()

	httpmock.RegisterMatcherResponder(
		http.MethodPost,
		fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/revert", MockServiceID, MockVolumeID),
		tdhttpmock.JSONBody(SnapshotRevertRequest{
			SnapshotID: MockSnapshotID,
		}),
		httpmock.NewBytesResponder(http.StatusAccepted, nil))
}

func TestClient_RestoreSnapshot(t *testing.T) {
	tests := []struct {
		mockFunction    func()
		volume          Volume
		snapshot        Snapshot
		restoreRequest  SnapshotRevertRequest
		isErrorExpected bool
	}{
		{
			mockFunction:    mockRestoreSnapshotResponse,
			volume:          Volume{ID: CreateVolumeID(MockServiceID, MockVolumeID)},
			snapshot:        Snapshot{ID: CreateSnapshotID(MockServiceID, MockVolumeID, MockSnapshotID)},
			restoreRequest:  SnapshotRevertRequest{SnapshotID: MockSnapshotID},
			isErrorExpected: false,
		},
		{
			mockFunction:    mockRestoreSnapshotResponse,
			volume:          Volume{ID: MockVolumeID},
			snapshot:        Snapshot{ID: CreateSnapshotID(MockServiceID, MockVolumeID, MockSnapshotID)},
			restoreRequest:  SnapshotRevertRequest{SnapshotID: MockSnapshotID},
			isErrorExpected: true,
		},
		{
			mockFunction:    mockRestoreSnapshotResponse,
			volume:          Volume{ID: CreateVolumeID(MockServiceID, MockVolumeID)},
			snapshot:        Snapshot{ID: MockSnapshotID},
			restoreRequest:  SnapshotRevertRequest{SnapshotID: MockSnapshotID},
			isErrorExpected: true,
		},
		{
			mockFunction:    mockUnAuthorizedResponseError,
			volume:          Volume{ID: CreateVolumeID(MockServiceID, MockVolumeID)},
			snapshot:        Snapshot{ID: CreateSnapshotID(MockServiceID, MockVolumeID, MockSnapshotID)},
			restoreRequest:  SnapshotRevertRequest{SnapshotID: MockSnapshotID},
			isErrorExpected: true,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("RestoreSnapshot %d", i), func(t *testing.T) {
			httpmock.Activate()
			d := getOVHClient()
			entry.mockFunction()

			err := d.RestoreSnapshot(ctx, &entry.volume, &entry.snapshot)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
			} else {
				assert.NoError(t, err, "snapshot restoration failed")
			}

			httpmock.DeactivateAndReset()
		})
	}
}

func mockDeleteSnapshotResponse() {
	mockGetOAuth2TokenResponse()

	httpmock.RegisterResponder(
		http.MethodDelete,
		fmt.Sprintf("https://eu.api.ovh.com/1.0/storage/netapp/%s/share/%s/snapshot/%s", MockServiceID, MockVolumeID, MockSnapshotID),
		httpmock.NewBytesResponder(http.StatusAccepted, nil))
}

func TestClient_DeleteSnapshot(t *testing.T) {
	tests := []struct {
		mockFunction    func()
		volume          Volume
		snapshot        Snapshot
		isErrorExpected bool
	}{
		{
			mockFunction:    mockDeleteSnapshotResponse,
			volume:          Volume{ID: CreateVolumeID(MockServiceID, MockVolumeID)},
			snapshot:        Snapshot{ID: CreateSnapshotID(MockServiceID, MockVolumeID, MockSnapshotID)},
			isErrorExpected: false,
		},
		{
			mockFunction:    mockDeleteSnapshotResponse,
			volume:          Volume{ID: MockVolumeID},
			snapshot:        Snapshot{ID: CreateSnapshotID(MockServiceID, MockVolumeID, MockSnapshotID)},
			isErrorExpected: true,
		},
		{
			mockFunction:    mockDeleteSnapshotResponse,
			volume:          Volume{ID: CreateVolumeID(MockServiceID, MockVolumeID)},
			snapshot:        Snapshot{ID: MockSnapshotID},
			isErrorExpected: true,
		},
		{
			mockFunction:    mockUnAuthorizedResponseError,
			volume:          Volume{ID: CreateVolumeID(MockServiceID, MockVolumeID)},
			snapshot:        Snapshot{ID: CreateSnapshotID(MockServiceID, MockVolumeID, MockSnapshotID)},
			isErrorExpected: true,
		},
	}

	for i, entry := range tests {
		t.Run(fmt.Sprintf("DeleteSnapshot %d", i), func(t *testing.T) {
			httpmock.Activate()
			d := getOVHClient()
			entry.mockFunction()

			err := d.DeleteSnapshot(ctx, &entry.volume, &entry.snapshot)

			if entry.isErrorExpected {
				assert.Error(t, err, "an error is expected")
			} else {
				assert.NoError(t, err, "snapshot deletion failed")
			}

			httpmock.DeactivateAndReset()
		})
	}
}
