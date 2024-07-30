// -*- Mode: Go; indent-tabs-mode: t -*-
//
// Copyright (C) 2020-2024 IOTech Ltd
//
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/edgexfoundry/device-sdk-go/v3/internal/application"
	"github.com/edgexfoundry/device-sdk-go/v3/internal/autodiscovery"
	"github.com/edgexfoundry/device-sdk-go/v3/internal/cache"
	"github.com/edgexfoundry/device-sdk-go/v3/internal/container"
	"github.com/edgexfoundry/device-sdk-go/v3/internal/controller/http/correlation"
	sdkModels "github.com/edgexfoundry/device-sdk-go/v3/pkg/models"
	"github.com/edgexfoundry/go-mod-bootstrap/v3/di"
	"github.com/edgexfoundry/go-mod-core-contracts/v3/common"
	commonDTO "github.com/edgexfoundry/go-mod-core-contracts/v3/dtos/common"
	"github.com/edgexfoundry/go-mod-core-contracts/v3/dtos/requests"
	"github.com/edgexfoundry/go-mod-core-contracts/v3/errors"
	"github.com/edgexfoundry/go-mod-core-contracts/v3/models"
)

func (c *RestController) Discovery(e echo.Context) error {
	request := e.Request()
	writer := e.Response()
	ds := container.DeviceServiceFrom(c.dic.Get)
	if ds.AdminState == models.Locked {
		err := errors.NewCommonEdgeX(errors.KindServiceLocked, "service locked", nil)
		return c.sendEdgexError(writer, request, err, common.ApiDiscoveryRoute)
	}

	configuration := container.ConfigurationFrom(c.dic.Get)
	if !configuration.Device.Discovery.Enabled {
		err := errors.NewCommonEdgeX(errors.KindServiceUnavailable, "device discovery disabled", nil)
		return c.sendEdgexError(writer, request, err, common.ApiDiscoveryRoute)
	}

	driver := container.ProtocolDriverFrom(c.dic.Get)

	correlationId := correlation.IdFromContext(request.Context())
	go func() {
		c.lc.Info("Discovery triggered.", common.CorrelationHeader, correlationId)
		go autodiscovery.DiscoveryWrapper(driver, c.lc)
		c.lc.Info("Discovery done.", common.CorrelationHeader, correlationId)
	}()
	response := commonDTO.NewBaseResponse("", "Trigger discovery with correlationId "+correlationId, http.StatusAccepted)
	return c.sendResponse(writer, request, common.ApiDiscoveryRoute, response, http.StatusAccepted)
}

func (c *RestController) ProfileScan(e echo.Context) error {
	request := e.Request()
	writer := e.Response()
	if request.Body != nil {
		defer func() { _ = request.Body.Close() }()
	}

	body, err := io.ReadAll(request.Body)
	if err != nil {
		edgexErr := errors.NewCommonEdgeX(errors.KindServerError, "Failed to read request body", err)
		return c.sendEdgexError(writer, request, edgexErr, common.ApiProfileScan)
	}

	ps := container.ProfileScanFrom(c.dic.Get)
	if ps == nil {
		edgexErr := errors.NewCommonEdgeX(errors.KindNotImplemented, "Profile scan is not implemented", nil)
		return c.sendEdgexError(writer, request, edgexErr, common.ApiProfileScan)
	}

	req, edgexErr := profileScanValidation(body, c.dic)
	if edgexErr != nil {
		return c.sendEdgexError(writer, request, edgexErr, common.ApiProfileScan)
	}

	correlationId := correlation.IdFromContext(request.Context())
	busy := make(chan bool)
	go func() {
		c.lc.Info("Profile scanning is triggered.", common.CorrelationHeader, correlationId)
		application.ProfileScanWrapper(busy, ps, req, c.dic)
		c.lc.Info("Profile scanning is done.", common.CorrelationHeader, correlationId)
	}()
	b := <-busy
	if b {
		edgexErr := errors.NewCommonEdgeX(errors.KindStatusConflict, fmt.Sprintf("Another profile scan process for %s is currently running", req.DeviceName), nil)
		return c.sendEdgexError(writer, request, edgexErr, common.ApiProfileScan)
	}

	response := commonDTO.NewBaseResponse("", "Trigger profile scan with correlationId "+correlationId, http.StatusAccepted)
	return c.sendResponse(writer, request, common.ApiProfileScan, response, http.StatusAccepted)
}

func profileScanValidation(request []byte, dic *di.Container) (sdkModels.ProfileScanRequest, errors.EdgeX) {
	var r sdkModels.ProfileScanRequest
	// check device service AdminState
	ds := container.DeviceServiceFrom(dic.Get)
	if ds.AdminState == models.Locked {
		return r, errors.NewCommonEdgeX(errors.KindServiceLocked, "service locked", nil)
	}

	// parse request payload
	var req requests.ProfileScanRequest
	err := req.UnmarshalJSON(request)
	if err != nil {
		return r, errors.NewCommonEdgeX(errors.KindContractInvalid, "failed to parse request body", err)
	}

	// check requested device exists
	device, ok := cache.Devices().ForName(req.DeviceName)
	if !ok {
		return r, errors.NewCommonEdgeX(errors.KindEntityDoesNotExist, fmt.Sprintf("device %s not found", req.DeviceName), nil)
	}

	// check profile should not exist
	if req.ProfileName != "" {
		if _, exist := cache.Profiles().ForName(req.ProfileName); exist {
			return r, errors.NewCommonEdgeX(errors.KindStatusConflict, fmt.Sprintf("profile name %s is duplicated", req.ProfileName), nil)
		}
	} else {
		req.ProfileName = fmt.Sprintf("%s_profile_%d", req.DeviceName, time.Now().UnixNano())
	}

	r = sdkModels.ProfileScanRequest{
		DeviceName:  req.DeviceName,
		ProfileName: req.ProfileName,
		Options:     req.Options,
		Protocols:   device.Protocols,
	}

	return r, nil
}
