package rpc

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	accessv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1/accessv1connect"
)

// APIKeys serves tanvo.access.v1.ApiKeyService over the API key service
// (lago GraphQL api keys resolvers and mutations).
type APIKeys struct {
	accessv1connect.UnimplementedApiKeyServiceHandler

	keys  *auth.APIKeys
	clock core.Clock
}

var _ accessv1connect.ApiKeyServiceHandler = (*APIKeys)(nil)

// NewAPIKeys returns the service over keys; clock times rotation overlaps.
func NewAPIKeys(keys *auth.APIKeys, clock core.Clock) *APIKeys {
	return &APIKeys{keys: keys, clock: clock}
}

var apiAccessLevels = NewEnum(map[permission.Level]accessv1.ApiAccessLevel{
	permission.LevelRead:  accessv1.ApiAccessLevel_API_ACCESS_LEVEL_READ,
	permission.LevelWrite: accessv1.ApiAccessLevel_API_ACCESS_LEVEL_WRITE,
})

// apiResourcePrefix prefixes the ApiResource value names, which are the
// upper-cased API resources.
const apiResourcePrefix = "API_RESOURCE_"

func apiResourceDescriptor() protoreflect.EnumDescriptor {
	return accessv1.ApiResource(0).Descriptor()
}

// CreateApiKey implements ApiKeyService.CreateApiKey (lago
// ApiKeys::CreateService); the response carries the secret, shown once.
func (s *APIKeys) CreateApiKey(ctx context.Context, req *accessv1.CreateApiKeyRequest) (*accessv1.CreateApiKeyResponse, error) { //nolint:revive // generated name
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetApiKey()
	grants, err := apiKeyGrants("api_key.grants", b.GetGrants())
	if err != nil {
		return nil, err
	}
	created, err := s.keys.Create(ctx, p.OrganizationID, Opt(b.HasName(), b.GetName()), grants)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.CreateApiKeyResponse_builder{ApiKey: createdKeyProto(created)}.Build(), nil
}

// GetApiKey implements ApiKeyService.GetApiKey (lago ApiKeyResolver): an
// expired key is not found.
func (s *APIKeys) GetApiKey(ctx context.Context, req *accessv1.GetApiKeyRequest) (*accessv1.GetApiKeyResponse, error) { //nolint:revive // generated name
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.APIKeyID]("api_key_id", req.GetApiKeyId())
	if err != nil {
		return nil, err
	}
	k, err := s.keys.Get(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.GetApiKeyResponse_builder{ApiKey: apiKeyProto(k)}.Build(), nil
}

// ListApiKeys implements ApiKeyService.ListApiKeys (lago ApiKeysResolver):
// the active keys.
func (s *APIKeys) ListApiKeys(ctx context.Context, req *accessv1.ListApiKeysRequest) (*accessv1.ListApiKeysResponse, error) { //nolint:revive // generated name
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	page, err := s.keys.List(ctx, p.OrganizationID, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.ListApiKeysResponse_builder{ApiKeys: Items(page, apiKeyProto), NextPageToken: &page.NextToken}.Build(), nil
}

// apiKeyUpdateFields are the update_mask paths of UpdateApiKey.
var apiKeyUpdateFields = []string{"name", "grants"}

// UpdateApiKey implements ApiKeyService.UpdateApiKey (lago
// ApiKeys::UpdateService).
func (s *APIKeys) UpdateApiKey(ctx context.Context, req *accessv1.UpdateApiKeyRequest) (*accessv1.UpdateApiKeyResponse, error) { //nolint:revive // generated name
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetApiKey()
	id, err := ParseID[core.APIKeyID]("api_key.id", b.GetId())
	if err != nil {
		return nil, err
	}
	paths, err := UpdatePaths(req.GetUpdateMask(), apiKeyUpdateFields, func(path string) bool {
		switch path {
		case "name":
			return b.HasName()
		case "grants":
			return len(b.GetGrants()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	var u auth.APIKeyUpdate
	if slices.Contains(paths, "name") {
		u.Name = new(b.GetName())
	}
	if slices.Contains(paths, "grants") {
		grants, err := apiKeyGrants("api_key.grants", b.GetGrants())
		if err != nil {
			return nil, err
		}
		if grants == nil {
			grants = map[permission.APIResource]permission.Level{}
		}
		u.Permissions = &grants
	}
	k, err := s.keys.Update(ctx, p.OrganizationID, id, u)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.UpdateApiKeyResponse_builder{ApiKey: apiKeyProto(k)}.Build(), nil
}

// RotateApiKey implements ApiKeyService.RotateApiKey (lago
// ApiKeys::RotateService): the old key expires at the end of the overlap,
// immediately without one.
func (s *APIKeys) RotateApiKey(ctx context.Context, req *accessv1.RotateApiKeyRequest) (*accessv1.RotateApiKeyResponse, error) { //nolint:revive // generated name
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.APIKeyID]("api_key_id", req.GetApiKeyId())
	if err != nil {
		return nil, err
	}
	rot := auth.APIKeyRotation{Name: Opt(req.HasName(), req.GetName())}
	if req.HasOverlap() {
		rot.ExpiresAt = new(s.clock.Now().UTC().Add(req.GetOverlap().AsDuration()))
	}
	created, err := s.keys.Rotate(ctx, p.OrganizationID, id, rot)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.RotateApiKeyResponse_builder{ApiKey: createdKeyProto(created)}.Build(), nil
}

// DeleteApiKey implements ApiKeyService.DeleteApiKey (lago
// ApiKeys::DestroyService): the key expires now; the organization's last
// non-expiring key cannot be deleted.
func (s *APIKeys) DeleteApiKey(ctx context.Context, req *accessv1.DeleteApiKeyRequest) (*accessv1.DeleteApiKeyResponse, error) { //nolint:revive // generated name
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.APIKeyID]("api_key_id", req.GetApiKeyId())
	if err != nil {
		return nil, err
	}
	if _, err := s.keys.Destroy(ctx, p.OrganizationID, id, false); err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.DeleteApiKeyResponse_builder{}.Build(), nil
}

// apiKeyGrants converts grants to key permissions; none is nil (full
// access). A resource granted twice is invalid.
func apiKeyGrants(field string, grants []*accessv1.ApiKeyGrant) (map[permission.APIResource]permission.Level, error) {
	if len(grants) == 0 {
		return nil, nil
	}
	out := make(map[permission.APIResource]permission.Level, len(grants))
	var violations []FieldViolation
	values := apiResourceDescriptor().Values()
	for i, g := range grants {
		v := values.ByNumber(protoreflect.EnumNumber(g.GetResource()))
		var res permission.APIResource
		if v != nil && g.GetResource() != 0 {
			res = permission.APIResource(strings.ToLower(strings.TrimPrefix(string(v.Name()), apiResourcePrefix)))
		}
		lvl := apiAccessLevels.Core(g.GetLevel())
		switch {
		case !res.Valid():
			violations = append(violations, FieldViolation{Field: fmt.Sprintf("%s[%d].resource", field, i), Reason: core.CodeInvalid})
		case lvl == "":
			violations = append(violations, FieldViolation{Field: fmt.Sprintf("%s[%d].level", field, i), Reason: core.CodeInvalid})
		default:
			if _, dup := out[res]; dup {
				violations = append(violations, FieldViolation{Field: fmt.Sprintf("%s[%d].resource", field, i), Reason: core.CodeAlreadyExists})
			}
			out[res] = lvl
		}
	}
	if len(violations) > 0 {
		return nil, InvalidArgument("invalid grants", violations)
	}
	return out, nil
}

func createdKeyProto(c *auth.CreatedAPIKey) *accessv1.ApiKey {
	k := apiKeyProto(c.Key)
	k.SetSecret(c.Secret)
	return k
}

func apiKeyProto(k *access.APIKey) *accessv1.ApiKey {
	if k == nil {
		return nil
	}
	resources := make([]permission.APIResource, 0, len(k.Permissions))
	for r := range k.Permissions {
		resources = append(resources, r)
	}
	slices.Sort(resources)
	values := apiResourceDescriptor().Values()
	grants := make([]*accessv1.ApiKeyGrant, 0, len(resources))
	for _, r := range resources {
		v := values.ByName(protoreflect.Name(apiResourcePrefix + strings.ToUpper(string(r))))
		if v == nil {
			continue
		}
		grants = append(grants, accessv1.ApiKeyGrant_builder{
			Resource: new(accessv1.ApiResource(v.Number())),
			Level:    new(apiAccessLevels.Proto(k.Permissions[r])),
		}.Build())
	}
	return accessv1.ApiKey_builder{
		Id:         new(k.ID.String()),
		Name:       k.Name,
		Prefix:     &k.Prefix,
		Grants:     grants,
		ExpiresAt:  TimestampPtr(k.ExpiresAt),
		LastUsedAt: TimestampPtr(k.LastUsedAt),
		CreatedAt:  Timestamp(k.CreatedAt),
		UpdatedAt:  Timestamp(k.UpdatedAt),
	}.Build()
}
