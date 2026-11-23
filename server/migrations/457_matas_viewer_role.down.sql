-- Viewers are removed from their workspaces before the old constraints come
-- back, so the rollback cannot fail on existing rows and no reader is
-- silently widened to member. Pending viewer invitations are expired.
UPDATE workspace_invitation SET status = 'expired', role = 'member'
    WHERE role = 'viewer';
ALTER TABLE workspace_invitation DROP CONSTRAINT IF EXISTS workspace_invitation_role_check;
ALTER TABLE workspace_invitation ADD CONSTRAINT workspace_invitation_role_check
    CHECK (role IN ('admin', 'member'));

DELETE FROM member WHERE role = 'viewer';
ALTER TABLE member DROP CONSTRAINT IF EXISTS member_role_check;
ALTER TABLE member ADD CONSTRAINT member_role_check
    CHECK (role IN ('owner', 'admin', 'member'));
