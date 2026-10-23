package auth

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
)

// memBlobs is an in-memory blob store.
type memBlobs struct {
	mu    sync.Mutex
	files map[core.BlobRef][]byte
	n     int
}

func (b *memBlobs) Put(_ context.Context, _ core.OrganizationID, name, _ string, body io.Reader) (core.BlobRef, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n++
	ref := core.BlobRef(fmt.Sprintf("%d/%s", b.n, name))
	if b.files == nil {
		b.files = map[core.BlobRef][]byte{}
	}
	b.files[ref] = data
	return ref, nil
}

func (b *memBlobs) Get(context.Context, core.OrganizationID, core.BlobRef) (io.ReadCloser, error) {
	return nil, core.ErrNotFound
}

func (b *memBlobs) Delete(_ context.Context, _ core.OrganizationID, ref core.BlobRef) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.files, ref)
	return nil
}

func (b *memBlobs) URL(_ context.Context, _ core.OrganizationID, ref core.BlobRef) (string, error) {
	return "https://blobs.tanvo.test/" + string(ref), nil
}

var pngLogo = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

func TestOrganizationSettingsUpdate(t *testing.T) {
	tests := []struct {
		name   string
		source string
		update OrganizationUpdate
		field  string
		code   string
		check  func(t *testing.T, o *organization.Organization)
	}{
		{
			name: "updates the organization", source: "spec/services/organizations/update_service_spec.rb:42",
			update: OrganizationUpdate{Email: core.Some("billing@hooli.com"), Address: core.Some(core.Address{City: "Paris", Country: "FR"})},
			check: func(t *testing.T, o *organization.Organization) {
				if o.Email == nil || *o.Email != "billing@hooli.com" || o.Address.City != "Paris" {
					t.Errorf("organization got = %+v", o)
				}
			},
		},
		{
			name: "sanitizes the email", source: "spec/services/organizations/update_service_spec.rb:63",
			update: OrganizationUpdate{Email: core.Some("hello@something–other.com")},
			check: func(t *testing.T, o *organization.Organization) {
				if *o.Email != "hello@something-other.com" {
					t.Errorf("email got = %q, want = %q", *o.Email, "hello@something-other.com")
				}
			},
		},
		{
			name: "updates the slug", source: "spec/services/organizations/update_service_spec.rb:125",
			update: OrganizationUpdate{Slug: core.Some("new-slug")},
			check: func(t *testing.T, o *organization.Organization) {
				if o.Slug != "new-slug" {
					t.Errorf("slug got = %q, want = new-slug", o.Slug)
				}
			},
		},
		{
			name: "normalizes the slug", source: "spec/services/organizations/update_service_spec.rb:136",
			update: OrganizationUpdate{Slug: core.Some("  My-Slug ")},
			check: func(t *testing.T, o *organization.Organization) {
				if o.Slug != "my-slug" {
					t.Errorf("slug got = %q, want = my-slug", o.Slug)
				}
			},
		},
		{
			name: "upper-cases the document number prefix", source: "spec/services/organizations/update_service_spec.rb:96",
			update: OrganizationUpdate{DocumentNumberPrefix: core.Some("abc")},
			check: func(t *testing.T, o *organization.Organization) {
				if o.DocumentNumberPrefix != "ABC" {
					t.Errorf("document_number_prefix got = %q, want = ABC", o.DocumentNumberPrefix)
				}
			},
		},
		{name: "document number prefix too long", source: "spec/services/organizations/update_service_spec.rb:113", update: OrganizationUpdate{DocumentNumberPrefix: core.Some("aaaaaaaaaaaaaaa")}, field: "document_number_prefix", code: core.CodeTooLong},
		{name: "invalid slug", source: "spec/services/organizations/update_service_spec.rb:147", update: OrganizationUpdate{Slug: core.Some("INVALID SLUG!")}, field: "slug"},
		{name: "slug taken", source: "spec/services/organizations/update_service_spec.rb:159", update: OrganizationUpdate{Slug: core.Some("taken-slug")}, field: "slug", code: core.CodeAlreadyExists},
		{name: "reserved slug", source: "spec/services/organizations/update_service_spec.rb:174", update: OrganizationUpdate{Slug: core.Some("customers")}, field: "slug"},
		{name: "short slug", source: "app/models/concerns/organizations/sluggable.rb:23", update: OrganizationUpdate{Slug: core.Some("ab")}, field: "slug", code: core.CodeTooShort},
		{name: "invalid country", source: "spec/services/organizations/update_service_spec.rb:283", update: OrganizationUpdate{Address: core.Some(core.Address{Country: "---"})}, field: "address.country"},
		{
			name: "base64 logo", source: "spec/services/organizations/update_service_spec.rb:269",
			update: OrganizationUpdate{Logo: core.Some(pngLogo)},
			check: func(t *testing.T, o *organization.Organization) {
				if o.Logo.IsZero() {
					t.Error("logo got = none, want a stored logo")
				}
			},
		},
		{name: "logo of another type", source: "app/models/organization.rb validates :logo, image", update: OrganizationUpdate{Logo: core.Some([]byte("GIF89a......"))}, field: "logo", code: CodeLogoInvalidContentType},
		{name: "logo too large", source: "app/models/organization.rb validates :logo, image", update: OrganizationUpdate{Logo: core.Some(append(slices.Clone(pngLogo), make([]byte, MaxOrganizationLogoSize)...))}, field: "logo", code: CodeLogoInvalidSize},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org()
			taken := e.org()
			if _, err := NewOrganizationSettings(e.deps, nil).Update(e.ctx, taken.ID, core.UserID{}, OrganizationUpdate{Slug: core.Some("taken-slug")}); err != nil {
				t.Fatal(err)
			}
			blobs := &memBlobs{}
			got, err := NewOrganizationSettings(e.deps, blobs).Update(e.ctx, o.ID, core.UserID{}, tt.update)
			if fieldOf(err) != tt.field {
				t.Fatalf("field got = %q (%v), want = %q (%s)", fieldOf(err), err, tt.field, tt.source)
			}
			if tt.code != "" {
				if fe := core.FieldErrors(err); len(fe) == 0 || fe[0].Code != tt.code {
					t.Errorf("code got = %v, want = %q (%s)", fe, tt.code, tt.source)
				}
			}
			if tt.field != "" {
				if len(blobs.files) != 0 {
					t.Errorf("blobs got = %d, want = 0", len(blobs.files))
				}
				return
			}
			tt.check(t, got)
			stored, err := NewOrganizationSettings(e.deps, nil).Get(e.ctx, o.ID)
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, stored)
		})
	}
}

func TestOrganizationSettingsLogo(t *testing.T) {
	e := newEnv(t)
	o := e.org()
	blobs := &memBlobs{}
	s := NewOrganizationSettings(e.deps, blobs)
	first, err := s.Update(e.ctx, o.ID, core.UserID{}, OrganizationUpdate{Logo: core.Some(pngLogo)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Update(e.ctx, o.ID, core.UserID{}, OrganizationUpdate{Logo: core.Some(pngLogo)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := blobs.files[first.Logo]; ok || len(blobs.files) != 1 {
		t.Errorf("blobs got = %v, want only %s", blobs.files, second.Logo)
	}
	u, err := s.LogoURL(e.ctx, second)
	if err != nil || u != "https://blobs.tanvo.test/"+string(second.Logo) {
		t.Errorf("LogoURL() got = %q, %v", u, err)
	}
	cleared, err := s.Update(e.ctx, o.ID, core.UserID{}, OrganizationUpdate{Logo: core.Some([]byte(nil))})
	if err != nil || !cleared.Logo.IsZero() || len(blobs.files) != 0 {
		t.Errorf("clear got = %v, %v, blobs %d", cleared.Logo, err, len(blobs.files))
	}
	if _, err := NewOrganizationSettings(e.deps, nil).Update(e.ctx, o.ID, core.UserID{}, OrganizationUpdate{Logo: core.Some(pngLogo)}); code(err) != CodeLogoStorageNotConfigured {
		t.Errorf("without blobs code got = %q, want = %q", code(err), CodeLogoStorageNotConfigured)
	}
}

func TestOrganizationSettingsAuthenticationMethods(t *testing.T) {
	// source: spec/services/organizations/update_service_spec.rb:388
	e := newEnv(t)
	o := e.org(organization.AuthenticationMethodEmailPassword, organization.AuthenticationMethodGoogleOAuth)
	actor := e.user("admin@hooli.com", "pw")
	e.member(actor, o, admin)
	e.mailJobs()
	s := NewOrganizationSettings(e.deps, nil)
	methods := []organization.AuthenticationMethod{organization.AuthenticationMethodEmailPassword, organization.AuthenticationMethodOkta}
	if _, err := s.Update(e.ctx, o.ID, actor.ID, OrganizationUpdate{AuthenticationMethods: core.Some(methods)}); err != nil {
		t.Fatal(err)
	}
	jobs := e.mailJobs()
	if len(jobs) != 1 || jobs[0].Template != MailAuthenticationMethodsUpdated {
		t.Fatalf("mail jobs got = %+v, want one %s", jobs, MailAuthenticationMethodsUpdated)
	}
	if d := jobs[0].Data; d["actor_email"] != "admin@hooli.com" || d["additions"] != "okta" || d["deletions"] != "google_oauth" {
		t.Errorf("data got = %v", d)
	}
	// Unchanged methods, or no user, send nothing (update_service.rb:39).
	if _, err := s.Update(e.ctx, o.ID, actor.ID, OrganizationUpdate{AuthenticationMethods: core.Some(methods)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(e.ctx, o.ID, core.UserID{}, OrganizationUpdate{AuthenticationMethods: core.Some(methods[:1])}); err != nil {
		t.Fatal(err)
	}
	if jobs := e.mailJobs(); len(jobs) != 0 {
		t.Errorf("mail jobs got = %+v, want none", jobs)
	}
}
