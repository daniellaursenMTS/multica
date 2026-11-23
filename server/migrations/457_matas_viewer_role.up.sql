-- Matas (MATA-286): the workspace role `viewer`, a reader who only uses the
-- MCP read tools and the project portal. Every route outside a named
-- allowlist refuses a viewer; see internal/middleware/viewer.go. Owners and
-- admins can invite a viewer; a share link still cannot grant one.
ALTER TABLE member DROP CONSTRAINT IF EXISTS member_role_check;
ALTER TABLE member ADD CONSTRAINT member_role_check
    CHECK (role IN ('owner', 'admin', 'member', 'viewer'));

ALTER TABLE workspace_invitation DROP CONSTRAINT IF EXISTS workspace_invitation_role_check;
ALTER TABLE workspace_invitation ADD CONSTRAINT workspace_invitation_role_check
    CHECK (role IN ('admin', 'member', 'viewer'));
