package dns

import "context"

type RecordType string

const (
	RecordA     RecordType = "A"
	RecordCNAME RecordType = "CNAME"
)

type DNSRecord struct {
	Name   string
	Type   RecordType
	Target string
	TTL    int
}

type DNSProvider interface {
	UpsertRecord(ctx context.Context, record DNSRecord) error
	DeleteRecord(ctx context.Context, record DNSRecord) error
}
