package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// deleteUser DELETE /users/:userId
//
// 超管删除指定用户，两个安全约束：
//  1. 不允许自删（防止意外锁死登录）
//  2. 不允许删除最后一个 super_admin（防止系统无人管理）
func (h *Handler) deleteUser(c *gin.Context) {
	targetID, err := strconv.ParseInt(c.Param("userId"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid user id")
		return
	}

	// 获取当前操作者 ID（来自 JWT）
	callerID, _ := c.Get("userID")
	if callerID.(int64) == targetID {
		fail(c, http.StatusBadRequest, "不能删除自己的账号")
		return
	}

	// 确认目标用户存在，并检查其角色
	var role string
	if err := h.db.QueryRow(`SELECT role FROM users WHERE id=?`, targetID).Scan(&role); err != nil {
		fail(c, http.StatusNotFound, "用户不存在")
		return
	}

	// 若目标是 super_admin，确保删除后至少还有一个 super_admin
	if role == "super_admin" {
		var count int
		h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='super_admin'`).Scan(&count)
		if count <= 1 {
			fail(c, http.StatusBadRequest, "不能删除最后一个超级管理员")
			return
		}
	}

	if _, err := h.db.Exec(`DELETE FROM users WHERE id=?`, targetID); err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}

	callerUID, callerName, callerRole := h.callerInfo(c)
	h.writeAuditLog(&callerUID, callerName, callerRole, "DELETE_USER", "删除用户 ID: "+strconv.FormatInt(targetID, 10), c.ClientIP())
	ok(c, gin.H{"deleted": true})
}

// changePassword PUT /users/:userId/password
//
// 超管为任意用户重置密码（含自己）。
// Body: {"password": "new_password_min8"}
func (h *Handler) changePassword(c *gin.Context) {
	targetID, err := strconv.ParseInt(c.Param("userId"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid user id")
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Password) == "" {
		fail(c, http.StatusBadRequest, "password required")
		return
	}
	if len(req.Password) < 8 {
		fail(c, http.StatusBadRequest, "密码不能少于 8 位")
		return
	}

	// 确认目标用户存在
	var exists int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id=?`, targetID).Scan(&exists); err != nil || exists == 0 {
		fail(c, http.StatusNotFound, "用户不存在")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		fail(c, http.StatusInternalServerError, "hash password failed")
		return
	}

	if _, err := h.db.Exec(`UPDATE users SET password_hash=? WHERE id=?`, string(hash), targetID); err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}

	callerUID, callerName, callerRole := h.callerInfo(c)
	h.writeAuditLog(&callerUID, callerName, callerRole, "CHANGE_PASSWORD", "重置用户 ID: "+strconv.FormatInt(targetID, 10)+" 的密码", c.ClientIP())
	ok(c, gin.H{"updated": true})
}
