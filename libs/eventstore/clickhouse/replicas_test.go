package clickhouse_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/dockertest"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// replicasScript starts, in one container, two ClickHouse servers that
// are replicas of shard s1 of the cluster tanvo, the first also running
// ClickHouse Keeper: replication without a container network.
const replicasScript = `set -e
for n in 1 2; do
  d=/etc/ch$n
  cp -r /etc/clickhouse-server $d
  mkdir -p /var/lib/ch$n
  cat > $d/config.d/zz-replica.xml <<X
<clickhouse>
  <listen_host>0.0.0.0</listen_host>
  <path>/var/lib/ch$n/</path>
  <tmp_path>/var/lib/ch$n/tmp/</tmp_path>
  <user_files_path>/var/lib/ch$n/user_files/</user_files_path>
  <format_schema_path>/var/lib/ch$n/format_schemas/</format_schema_path>
  <logger><log>/var/log/ch$n.log</log><errorlog>/var/log/ch$n.err.log</errorlog><level>warning</level><console>0</console></logger>
  <http_port>812$n</http_port>
  <tcp_port>900$n</tcp_port>
  <mysql_port remove="1"/>
  <postgresql_port remove="1"/>
  <interserver_http_port>901$n</interserver_http_port>
  <interserver_http_host>127.0.0.1</interserver_http_host>
  <macros><shard>s1</shard><replica>r$n</replica></macros>
  <zookeeper><node><host>127.0.0.1</host><port>9181</port></node></zookeeper>
  <remote_servers><tanvo><shard><internal_replication>true</internal_replication>
    <replica><host>127.0.0.1</host><port>9001</port></replica>
    <replica><host>127.0.0.1</host><port>9002</port></replica>
  </shard></tanvo></remote_servers>
</clickhouse>
X
done
cat > /etc/ch1/config.d/zz-keeper.xml <<X
<clickhouse><keeper_server>
  <tcp_port>9181</tcp_port><server_id>1</server_id>
  <log_storage_path>/var/lib/ch1/coordination/log</log_storage_path>
  <snapshot_storage_path>/var/lib/ch1/coordination/snapshots</snapshot_storage_path>
  <raft_configuration><server><id>1</id><hostname>127.0.0.1</hostname><port>9234</port></server></raft_configuration>
</keeper_server></clickhouse>
X
chown -R clickhouse:clickhouse /var/lib/ch1 /var/lib/ch2 /etc/ch1 /etc/ch2 /var/log
su -s /bin/bash clickhouse -c "clickhouse-server --config-file=/etc/ch1/config.xml" &
su -s /bin/bash clickhouse -c "clickhouse-server --config-file=/etc/ch2/config.xml" &
wait -n
cat /var/log/ch*.err.log
exit 1
`

// startReplicas starts the replicas and creates the database tanvo, of the
// Replicated engine, on both. It returns the container and the address of
// each replica.
func startReplicas(ctx context.Context, engine *dockertest.Engine) (*dockertest.Container, []string, error) {
	c, err := engine.Run(ctx, dockertest.Spec{
		Name:  "clickhouse-replicas",
		Image: clickhouseImage,
		Cmd:   []string{"bash", "-c", replicasScript},
		Ports: []string{"9001/tcp", "9002/tcp"},
	})
	if err != nil {
		return nil, nil, err
	}
	addrs := []string{c.Addr("9001/tcp"), c.Addr("9002/tcp")}
	conn, err := open(ctx, "clickhouse://"+addrs[0]+"/default")
	if err != nil {
		_ = c.Close(context.Background())
		return nil, nil, err
	}
	defer conn.Close()
	// The cluster is up once Keeper serves both replicas.
	err = dockertest.Wait(ctx, 2*time.Minute, func(ctx context.Context) error {
		return conn.Exec(ctx, `CREATE DATABASE IF NOT EXISTS tanvo ON CLUSTER tanvo ENGINE = Replicated('/clickhouse/databases/tanvo', '{shard}', '{replica}')`)
	})
	if err != nil {
		_ = c.Close(context.Background())
		return nil, nil, fmt.Errorf("create the replicated database: %w", err)
	}
	return c, addrs, nil
}

func replicas(t *testing.T) {
	t.Helper()
	database(t)
	if testDB.replicas == nil {
		t.Skip("needs TANVO_TEST_CLICKHOUSE_REPLICATED=1")
	}
}

// TestReplicatedEngines checks that Migrate created every table of a
// Replicated database with a replicated engine.
func TestReplicatedEngines(t *testing.T) {
	replicas(t)
	for i, conn := range testDB.replicas {
		rows, err := conn.Query(t.Context(), "SELECT name, engine FROM system.tables WHERE database = 'tanvo' ORDER BY name")
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var name, engine string
			if err := rows.Scan(&name, &engine); err != nil {
				t.Fatal(err)
			}
			n++
			if !strings.HasPrefix(engine, "Replicated") {
				t.Errorf("engine of %s on replica %d got = %s, want = Replicated…", name, i+1, engine)
			}
		}
		rows.Close()
		if n == 0 {
			t.Errorf("tables on replica %d got = none, want = the schema's", i+1)
		}
	}
}

// TestReplicaReadsAcknowledgedWrites writes through one replica and reads
// at once through the other: the read sees every write the quorum
// acknowledged, events and buckets, without waiting for replication.
func TestReplicaReadsAcknowledgedWrites(t *testing.T) {
	replicas(t)
	writer, reader := clickhouse.New(testDB.replicas[0]), clickhouse.New(testDB.replicas[1])
	def := metering.BucketDefinition{OrganizationID: "org", Code: "code"}
	if err := writer.DefineBuckets(t.Context(), def); err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)
	q := metering.Query{OrganizationID: "org", SubscriptionID: "sub", Code: "code", From: ts, To: ts.Add(24 * time.Hour)}
	for i := range 20 {
		if _, err := writer.Write(t.Context(), []metering.Event{event(fmt.Sprint("tx", i), ts.Add(time.Duration(i)*time.Minute))}); err != nil {
			t.Fatal(err)
		}
		n, err := reader.Count(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		if n != int64(i+1) {
			t.Fatalf("count on the other replica after write %d got = %d, want = %d", i+1, n, i+1)
		}
		totals, ok, err := reader.ReadBuckets(t.Context(), metering.BucketRead{Query: metering.Query{OrganizationID: "org", SubscriptionID: "sub", Code: "code"}, From: ts.Truncate(24 * time.Hour), To: ts.Truncate(24 * time.Hour).Add(24 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if !ok || len(totals) != 1 || totals[0].Count != int64(i+1) {
			t.Fatalf("buckets on the other replica after write %d got = %+v (served %t), want = one total of count %d", i+1, totals, ok, i+1)
		}
	}
}
