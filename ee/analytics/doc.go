// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

// Package analytics is the enterprise analytics service, tanvo-analytics
// (architecture §14.1): it serves the Data API (tanvo.dataapi.v1) over the
// core's export views and its own analytics schema.
//
// The subpackages split it: period cuts ranges into days, weeks and
// months; mrr derives each subscription's MRR over time; currency holds
// the minor-unit exponents; servicetoken verifies the core's service
// tokens; store owns the analytics schema, its rebuild and the report
// queries; server is the Connect service.
package analytics
