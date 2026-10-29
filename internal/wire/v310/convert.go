// SPDX-License-Identifier: MIT

package v310

import (
	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire/common"
)

// What 3.1.0 writes as 3.0.1 does lives in package common; these names
// keep the code of this package readable as the schema of one revision.

type (
	metadata                = common.Metadata
	intervalPeriod          = common.IntervalPeriod
	valuesMap               = common.ValuesMap
	interval                = common.Interval
	eventPayloadDescriptor  = common.EventPayloadDescriptor
	reportPayloadDescriptor = common.ReportPayloadDescriptor
	reportResource          = common.ReportResource
)

var (
	invalid              = common.Invalid
	decode               = common.Decode
	metaTo               = common.MetaTo
	metaFrom             = common.MetaFrom
	durationTo           = common.DurationTo
	durationFrom         = common.DurationFrom
	periodTo             = common.PeriodTo
	periodFrom           = common.PeriodFrom
	valuesMapsTo         = common.ValuesMapsTo
	valuesMapsFrom       = common.ValuesMapsFrom
	intervalsTo          = common.IntervalsTo
	intervalsFrom        = common.IntervalsFrom
	eventDescriptorTo    = common.EventDescriptorTo
	eventDescriptorFrom  = common.EventDescriptorFrom
	reportDescriptorTo   = common.ReportDescriptorTo
	reportDescriptorFrom = common.ReportDescriptorFrom
)

func mapSlice[A, B any](in []A, f func(A) B) []B { return common.MapSlice(in, f) }

func requestDescriptorTo(d openadr.ReportDescriptor) reportDescriptor {
	ri := string(d.ReportIntervals)
	return reportDescriptor{ReportDescriptorCommon: common.RequestDescriptorTo(d), Targets: d.Targets, ReportIntervals: &ri}
}

func requestDescriptorFrom(d reportDescriptor) openadr.ReportDescriptor {
	out := common.RequestDescriptorFrom(d.ReportDescriptorCommon)
	out.Targets = d.Targets
	if d.ReportIntervals != nil {
		out.ReportIntervals = openadr.ReportIntervals(*d.ReportIntervals)
	}
	return out
}
