// Package permission is the permission catalog of Tanvo (domain §5.3).
//
// A [Permission] is a string "<resource>:<action>"; [All] lists the
// catalog, ported from lago's config/permissions.yml with the action
// "view" renamed "read". Built-in roles ([BuiltInRole]: admin, finance,
// manager) hold fixed subsets: admin holds every permission.
//
// API keys use a coarser model: a [Level] (read or write) per
// [APIResource]; [LevelForAction] maps a role action to the level an API
// key needs.
package permission
