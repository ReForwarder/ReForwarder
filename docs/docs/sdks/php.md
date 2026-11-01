---
sidebar_position: 3
title: PHP
description: Posta PHP SDK
---

# PHP SDK

Official PHP client for Posta.

## Installation

```bash
composer require goposta/posta-php
```

**Requires:** PHP 8.1+, ext-curl, ext-json

## Quick Start

```php
<?php

use Posta\PostaClient;

$client = new PostaClient('https://posta.example.com', 'your-api-key');

$response = $client->sendEmail([
    'from' => 'sender@example.com',
    'to' => ['recipient@example.com'],
    'subject' => 'Hello from Posta',
    'html' => '<h1>Hello!</h1><p>This is a test email.</p>',
]);

echo "Email sent: id={$response['id']} status={$response['status']}\n";
```

## Send Template Email

```php
$response = $client->sendTemplateEmail([
    'template' => 'welcome',
    'to' => ['user@example.com'],
    'from' => 'noreply@example.com',
    'language' => 'en',
    'template_data' => [
        'name' => 'Alice',
    ],
]);
```

## Batch Send

```php
$response = $client->sendBatch([
    'template' => 'newsletter',
    'from' => 'news@example.com',
    'recipients' => [
        ['email' => 'user1@example.com', 'template_data' => ['name' => 'Bob']],
        ['email' => 'user2@example.com', 'language' => 'fr', 'template_data' => ['name' => 'Carol']],
    ],
]);

echo "Sent: {$response['sent']}, Failed: {$response['failed']}\n";
```

## Check Delivery Status

```php
$status = $client->getEmailStatus('email-uuid');
echo "Status: {$status['status']}\n";
```

## Error Handling

```php
use Posta\PostaException;

try {
    $client->getEmailStatus('invalid-uuid');
} catch (PostaException $e) {
    echo "Status: {$e->getStatusCode()}\n";
    $info = $e->getErrorInfo();
    if ($info) {
        echo "Message: {$info['message']}\n";
    }
}
```
