package toolstats

import (
	"context"
	"database/sql"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Metadata contains explicit execution context, never request/response payloads.
type Metadata struct {
	Client          string `json:"client,omitempty"`
	ClientVersion   string `json:"client_version,omitempty"`
	Profile         string `json:"profile,omitempty"`
	ProviderVersion string `json:"provider_version,omitempty"`
	SchemaHash      string `json:"schema_hash,omitempty"`
	Origin          string `json:"origin"`
	ReceiptID       string `json:"receipt_id,omitempty"`
}

type metadataKey struct{}

// WithMetadata attaches explicit execution context to subsequent observations.
func WithMetadata(ctx context.Context, m Metadata) context.Context {
	return context.WithValue(ctx, metadataKey{}, m)
}

const metadataSchema = `
CREATE TABLE IF NOT EXISTS event_context (
 event TEXT PRIMARY KEY, client TEXT NOT NULL, client_version TEXT NOT NULL,
 profile TEXT NOT NULL, provider_version TEXT NOT NULL, schema_hash TEXT NOT NULL,
 origin TEXT NOT NULL, receipt TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS catalog_context (
 provider TEXT PRIMARY KEY, version TEXT NOT NULL, schema_hash TEXT NOT NULL, observed INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS context_rollups (
 bucket INTEGER NOT NULL,level TEXT NOT NULL,tool TEXT NOT NULL,provider TEXT NOT NULL,repository TEXT NOT NULL,
 client TEXT NOT NULL,client_version TEXT NOT NULL,profile TEXT NOT NULL,provider_version TEXT NOT NULL,schema_hash TEXT NOT NULL,origin TEXT NOT NULL,code TEXT NOT NULL,
 calls INTEGER NOT NULL,ok INTEGER NOT NULL,refused INTEGER NOT NULL,fail INTEGER NOT NULL,cancel INTEGER NOT NULL,dsum INTEGER NOT NULL,samples INTEGER NOT NULL,dmax INTEGER NOT NULL,last INTEGER NOT NULL,max_ended INTEGER NOT NULL,
 PRIMARY KEY(bucket,level,tool,provider,repository,client,client_version,profile,provider_version,schema_hash,origin,code));
`

func (m Metadata) clean() Metadata {
	m.Client = Clean(contract.RedactRaw(m.Client), 120)
	m.ClientVersion = Clean(contract.RedactRaw(m.ClientVersion), 80)
	m.Profile = Clean(contract.RedactRaw(m.Profile), 120)
	m.ProviderVersion = Clean(contract.RedactRaw(m.ProviderVersion), 80)
	m.SchemaHash = Clean(m.SchemaHash, 80)
	m.ReceiptID = Clean(m.ReceiptID, 100)
	if m.Origin != "normal" && m.Origin != "synthetic" {
		m.Origin = "unknown"
	}
	return m
}

func writeMetadata(tx *sql.Tx, e Event) error {
	m := e.Metadata.clean()
	_, err := tx.Exec(`INSERT INTO event_context VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(event) DO UPDATE SET client=excluded.client,client_version=excluded.client_version,profile=excluded.profile,provider_version=excluded.provider_version,schema_hash=excluded.schema_hash,origin=excluded.origin,receipt=excluded.receipt`, e.ID, m.Client, m.ClientVersion, m.Profile, m.ProviderVersion, m.SchemaHash, m.Origin, m.ReceiptID)
	return err
}

func foldContext(tx *sql.Tx, cut int64) error {
	_, err := tx.Exec(`INSERT INTO context_rollups SELECT (at/86400000000)*86400000000,level,tool,provider,repository,
 coalesce(client,''),coalesce(client_version,''),coalesce(profile,''),coalesce(provider_version,''),coalesce(schema_hash,''),coalesce(origin,'unknown'),code,
 count(*),sum(outcome='ok'),sum(outcome='refused'),sum(outcome='fail'),sum(outcome='cancel'),
 coalesce(sum(CASE WHEN outcome!='cancel' AND duration>=0 THEN duration END),0),sum(outcome!='cancel' AND duration>=0),
 coalesce(max(CASE WHEN outcome!='cancel' AND duration>=0 THEN duration END),0),max(at),max(ended)
 FROM events LEFT JOIN event_context ON events.id=event_context.event WHERE at<? AND ended IS NOT NULL GROUP BY 1,2,3,4,5,6,7,8,9,10,11,12
 ON CONFLICT DO UPDATE SET calls=calls+excluded.calls,ok=ok+excluded.ok,refused=refused+excluded.refused,fail=fail+excluded.fail,cancel=cancel+excluded.cancel,dsum=dsum+excluded.dsum,samples=samples+excluded.samples,dmax=max(dmax,excluded.dmax),last=max(last,excluded.last),max_ended=max(max_ended,excluded.max_ended)`, cut)
	return err
}

// RememberIdentity records discovery evidence without causing discovery itself.
func (s *Store) RememberIdentity(provider, version, hash string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.writer()
	if err == nil {
		_, err = db.Exec(`INSERT INTO catalog_context VALUES(?,?,?,?) ON CONFLICT(provider) DO UPDATE SET version=excluded.version,schema_hash=excluded.schema_hash,observed=excluded.observed`, provider, Clean(version, 80), Clean(hash, 80), time.Now().UnixMicro())
	}
	if err != nil {
		s.dropped.Add(1)
	}
}
