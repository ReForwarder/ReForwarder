---
sidebar_position: 6
title: Session Management
description: Manage active sessions
---

# Session Management

Posta tracks active sessions and provides tools to manage them.

## List Active Sessions

```
GET /api/v1/users/me/sessions
```

Response:

```json
{
  "success": true,
  "data": [
    {
      "id": "session-uuid",
      "user_agent": "Mozilla/5.0 ...",
      "ip_address": "203.0.113.42",
      "created_at": "2026-01-01T00:00:00Z",
      "expires_at": "2026-01-02T00:00:00Z",
      "revoked": false
    }
  ]
}
```

## Revoke a Session

```
DELETE /api/v1/users/me/sessions/{sessionId}
```

## Revoke All Other Sessions

Log out all sessions except the current one:

```
POST /api/v1/users/me/sessions/revoke-others
```

## Logout

Invalidate the current session:

```
POST /api/v1/users/me/sessions/logout
```

## How Sessions Work

- Sessions are backed by JWT tokens with a 24-hour TTL
- Token revocation is tracked via a Redis-backed blacklist
- Revoked tokens are immediately rejected, even if not yet expired
