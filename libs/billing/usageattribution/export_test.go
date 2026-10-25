package usageattribution

// SetLimits lowers the most groups of a query and entries of a price
// lookup (lago's stubbed MAX_GROUPS and MAX_ENTRIES).
func (s *QueryService) SetLimits(groups, entries int) {
	s.maxGroups, s.maxLookupEntries = groups, entries
}
