-- Instance-level admins gate host-wide operations such as version checks and
-- self-upgrades. Team/project admins only own application resources.
ALTER TABLE users
    ADD COLUMN is_instance_admin BOOLEAN NOT NULL DEFAULT false;

-- Preserve existing installs by promoting exactly one current operator.
-- Greenfield installs still rely on /auth/register promoting the first user.
UPDATE users
   SET is_instance_admin = true
 WHERE id = (
    SELECT id
      FROM users
     ORDER BY created_at ASC, id ASC
     LIMIT 1
 );
