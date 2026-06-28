-- 逐表还原为单列 FK（DROP 复合 FK + ADD 单列 FK 交替）；两个 UNIQUE 约束置最后删除
-- （此时所有引用它们的复合 FK 已卸除）。
ALTER TABLE user_role_binding DROP CONSTRAINT fk_user_role_binding_role_app;
ALTER TABLE user_role_binding ADD CONSTRAINT fk_user_role_binding_role
    FOREIGN KEY (role_id) REFERENCES role(id);

ALTER TABLE role_inheritance DROP CONSTRAINT fk_role_inheritance_parent_app;
ALTER TABLE role_inheritance DROP CONSTRAINT fk_role_inheritance_child_app;
ALTER TABLE role_inheritance ADD CONSTRAINT fk_role_inheritance_parent
    FOREIGN KEY (parent_role_id) REFERENCES role(id);
ALTER TABLE role_inheritance ADD CONSTRAINT fk_role_inheritance_child
    FOREIGN KEY (child_role_id) REFERENCES role(id);

ALTER TABLE role_permission DROP CONSTRAINT fk_role_permission_role_app;
ALTER TABLE role_permission DROP CONSTRAINT fk_role_permission_permission_app;
ALTER TABLE role_permission ADD CONSTRAINT fk_role_permission_role
    FOREIGN KEY (role_id) REFERENCES role(id);
ALTER TABLE role_permission ADD CONSTRAINT fk_role_permission_permission
    FOREIGN KEY (permission_id) REFERENCES permission(id);

ALTER TABLE role       DROP CONSTRAINT uq_role_app_id;
ALTER TABLE permission DROP CONSTRAINT uq_permission_app_id;
