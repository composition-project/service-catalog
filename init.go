// Copyright 2014-2016 Fraunhofer Institute for Applied Information Technology FIT

package main

import (
	"github.com/farshidtz/elog"
	"github.com/linksmart/service-catalog/catalog"
)

var logger *elog.Logger

func init() {
	logger = elog.New(catalog.LoggerPrefix, &elog.Config{
		DebugPrefix: catalog.LoggerPrefix,
		DebugTrace:  elog.NoTrace,
	})
}
