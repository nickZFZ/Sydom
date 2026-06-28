-- 把 role/permission 的 app 局部性下沉到 DB（防御纵深，补「DB 真相源」）。
-- 前置：既有跨 app 引用 → RAISE 拒迁（fail-close，不静默修复）。
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM role_permission rp JOIN role r ON r.id = rp.role_id WHERE r.app_id <> rp.app_id) THEN
        RAISE EXCEPTION 'role_permission has cross-app role references; aborting';
    END IF;
    IF EXISTS (SELECT 1 FROM role_permission rp JOIN permission p ON p.id = rp.permission_id WHERE p.app_id <> rp.app_id) THEN
        RAISE EXCEPTION 'role_permission has cross-app permission references; aborting';
    END IF;
    IF EXISTS (SELECT 1 FROM role_inheritance ri JOIN role r ON r.id = ri.parent_role_id WHERE r.app_id <> ri.app_id) THEN
        RAISE EXCEPTION 'role_inheritance has cross-app parent references; aborting';
    END IF;
    IF EXISTS (SELECT 1 FROM role_inheritance ri JOIN role r ON r.id = ri.child_role_id WHERE r.app_id <> ri.app_id) THEN
        RAISE EXCEPTION 'role_inheritance has cross-app child references; aborting';
    END IF;
    IF EXISTS (SELECT 1 FROM user_role_binding b JOIN role r ON r.id = b.role_id WHERE r.app_id <> b.app_id) THEN
        RAISE EXCEPTION 'user_role_binding has cross-app role references; aborting';
    END IF;
END $$;

-- 复合 FK 目标：app 内唯一键（id 已是 PK，此唯一键供 (app_id,id) 复合 FK 引用）。
ALTER TABLE role       ADD CONSTRAINT uq_role_app_id       UNIQUE (app_id, id);
ALTER TABLE permission ADD CONSTRAINT uq_permission_app_id UNIQUE (app_id, id);

-- role_permission：单列 FK → 复合 FK（ADD 时 PG 校验既有行，违例则迁移失败）。
ALTER TABLE role_permission DROP CONSTRAINT fk_role_permission_role;
ALTER TABLE role_permission DROP CONSTRAINT fk_role_permission_permission;
ALTER TABLE role_permission ADD CONSTRAINT fk_role_permission_role_app
    FOREIGN KEY (app_id, role_id) REFERENCES role(app_id, id);
ALTER TABLE role_permission ADD CONSTRAINT fk_role_permission_permission_app
    FOREIGN KEY (app_id, permission_id) REFERENCES permission(app_id, id);

-- role_inheritance：parent/child 复合 FK。
ALTER TABLE role_inheritance DROP CONSTRAINT fk_role_inheritance_parent;
ALTER TABLE role_inheritance DROP CONSTRAINT fk_role_inheritance_child;
ALTER TABLE role_inheritance ADD CONSTRAINT fk_role_inheritance_parent_app
    FOREIGN KEY (app_id, parent_role_id) REFERENCES role(app_id, id);
ALTER TABLE role_inheritance ADD CONSTRAINT fk_role_inheritance_child_app
    FOREIGN KEY (app_id, child_role_id) REFERENCES role(app_id, id);

-- user_role_binding：role 复合 FK。
ALTER TABLE user_role_binding DROP CONSTRAINT fk_user_role_binding_role;
ALTER TABLE user_role_binding ADD CONSTRAINT fk_user_role_binding_role_app
    FOREIGN KEY (app_id, role_id) REFERENCES role(app_id, id);
