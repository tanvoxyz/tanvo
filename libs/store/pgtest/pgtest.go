// Package pgtest gives tests a fresh PostgreSQL 18 database each.
//
// One server serves every test of a package: the server named by
// TANVO_TEST_POSTGRES_DSN (a superuser DSN), or else a postgres:18
// container started on the local Docker engine at the first use. The engine
// is found through DOCKER_HOST, the current docker context, or the usual
// sockets (OrbStack, Docker Desktop, /var/run/docker.sock). Packages that
// use pgtest call [Main] from TestMain so that the container is removed when
// the tests end:
//
//	func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }
//
// [New] creates one database per test from a template built once per
// process by the [Template]'s setup function (typically applying the
// migrations), and drops it when the test ends. Tests are skipped when
// testing.Short is set or no server is reachable.
//
// Every server gets the login role [AppRole], which is neither a superuser
// nor exempt from row-level security; templates grant it the usual DML
// privileges, so tests can connect as an application would in Tanvo Cloud.
package pgtest
