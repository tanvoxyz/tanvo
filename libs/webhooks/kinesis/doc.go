// Package kinesis is the Amazon Kinesis producer of streaming destinations
// (parity §1.40): it implements port.StreamProducer by writing each record
// with the Kinesis PutRecord API, signed with AWS Signature Version 4,
// under the credentials of the destination's IAM role, assumed with STS
// AssumeRole (through an optional intermediate role) from the process's
// ambient AWS identity.
//
// It speaks the two AWS protocols it needs over net/http rather than
// through the AWS SDK: Kinesis JSON 1.1 and the STS Query API. The ambient
// identity is resolved from the environment variables, a web identity
// token file (EKS IAM roles for service accounts), the container
// credentials endpoint (ECS, EKS Pod Identity) or the EC2 instance
// metadata service (IMDSv2).
package kinesis
