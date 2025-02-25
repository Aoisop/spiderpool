// Copyright 2022 Authors of spidernet-io
// SPDX-License-Identifier: Apache-2.0

package subnetmanager

import (
	"time"

	"github.com/spidernet-io/spiderpool/pkg/config"
)

type SubnetConfig struct {
	config.UpdateCRConfig
	EnableSpiderSubnet bool

	LeaderRetryElectGap time.Duration
}
