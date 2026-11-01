---
sidebar_position: 1
title: Overview
description: Email templates with versioning and localization
---

# Templates

Posta provides a full-featured template system with version control, multi-language support, and live preview.

## Capabilities

- **Version control** — Maintain multiple versions of each template and activate any version at any time
- **Multi-language** — Add localized versions for each template version
- **Variable substitution** — Use `{{variable_name}}` syntax for dynamic content
- **CSS inlining** — Managed stylesheets are automatically inlined for email client compatibility
- **Preview & test** — Render templates with sample data before sending
- **Import/export** — Move templates between environments as JSON

## Template Structure

```
Template
├── Name, Description
├── Default Language
├── Active Version ──▶ Version 1 (active)
│                      ├── HTML body
│                      ├── Text body
│                      ├── Sample data
│                      └── Localizations
│                          ├── English (en)
│                          ├── French (fr)
│                          └── German (de)
├── Version 2
│   ├── HTML body
│   └── Localizations
│       └── English (en)
└── Version 3 (draft)
```

## Variable Syntax

Templates use double curly braces for variable substitution:

```html
<h1>Hello, {{name}}!</h1>
<p>Your order #{{order_id}} has been shipped.</p>
<p>Track your package: <a href="{{tracking_url}}">Click here</a></p>
```

Variables are provided via the `template_data` field when sending.

## Sections

- [Creating Templates](/docs/templates/creating-templates) — Create and manage templates
- [Versioning](/docs/templates/versioning) — Work with template versions
- [Localization](/docs/templates/localization) — Add multi-language support
- [Stylesheets](/docs/templates/stylesheets) — Manage CSS for email templates
- [Preview & Test](/docs/templates/preview-and-test) — Preview and test send
- [Import/Export](/docs/templates/import-export) — Move templates between environments
