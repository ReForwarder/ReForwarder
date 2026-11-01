---
sidebar_position: 4
title: Two-Factor Authentication
description: TOTP-based two-factor authentication
---

# Two-Factor Authentication

Posta supports TOTP (Time-based One-Time Password) for two-factor authentication, compatible with Google Authenticator, Authy, and other TOTP apps.

## Setup 2FA

### Step 1: Generate Secret

```
POST /api/v1/users/me/2fa/setup
```

Response:

```json
{
  "success": true,
  "data": {
    "secret": "JBSWY3DPEHPK3PXP",
    "url": "otpauth://totp/Posta:user@example.com?secret=JBSWY3DPEHPK3PXP&issuer=Posta"
  }
}
```

Use the `url` to generate a QR code, or manually enter the `secret` in your authenticator app.

### Step 2: Verify and Enable

Enter the code from your authenticator app:

```
POST /api/v1/users/me/2fa/verify
```

```json
{
  "code": "123456"
}
```

2FA is now enabled. All future logins will require a TOTP code.

## Login with 2FA

Include the `two_factor_code` field when logging in:

```json
{
  "email": "user@example.com",
  "password": "your-password",
  "two_factor_code": "123456"
}
```

## Disable 2FA

```
POST /api/v1/users/me/2fa/disable
```

```json
{
  "code": "123456"
}
```

A valid TOTP code is required to disable 2FA.

## Admin: Disable 2FA for a User

Administrators can disable 2FA for any user (e.g., if they lose their authenticator):

```
DELETE /api/v1/admin/users/{userId}/2fa
```
