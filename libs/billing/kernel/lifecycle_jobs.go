package kernel

// JobEndTrials ends the free trials that ended by an instant and bills the
// in-advance base fee of their plans (domain §9.4); its args are
// [InstantArgs].
const JobEndTrials = "billing.end_trials"
