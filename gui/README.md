# TenantEnvironment GUI

> **Note**: This entire GUI application was generated and coded by AI (GitHub Copilot).

A simple web interface for managing TenantEnvironment Custom Resources in Kubernetes.

## Purpose

This GUI provides an easy-to-use web interface for:
- **Viewing** all TenantEnvironment CRs in your cluster
- **Creating** new tenant environments with a user-friendly form
- **Editing** existing tenant configurations
- **Deleting** tenant environments

## Features

- Clean, responsive web interface built with Express.js and Tailwind CSS
- Full CRUD operations for TenantEnvironment custom resources
- Form validation and error handling
- Visual indicators for database configuration (dedicated vs shared)
- Color-coded performance tier badges

## Prerequisites

1. **kubectl context**: Your `kubectl` context must be set to the cluster you want to manage
   ```bash
   kubectl config current-context
   kubectl config use-context <your-cluster-context>
   ```

2. **TenantEnvironment CRD**: The TenantEnvironment Custom Resource Definition must be installed on your cluster
   ```bash
   # Install the CRD (run from the operator-demo root directory)
   make install
   ```

3. **Node.js**: Node.js 16+ required to run the application

## Installation

1. Navigate to the GUI directory:
   ```bash
   cd gui
   ```

2. Install dependencies:
   ```bash
   npm install
   ```

## Usage

1. Start the server:
   ```bash
   node app.js
   ```

2. Open your browser and navigate to:
   ```
   http://localhost:3000
   ```

3. Use the web interface to manage your TenantEnvironment resources

## Configuration

The GUI connects to your cluster using the current kubectl context. By default, it looks for TenantEnvironment CRs in the `default` namespace. You can modify the namespace in `app.js` if needed:

```javascript
const namespace = 'default'  // Change this to your desired namespace
```

## Notes

- The GUI uses the Kubernetes JavaScript client to interact with the cluster
- All operations respect Kubernetes RBAC permissions
- Changes made through the GUI are immediately reflected in your cluster
