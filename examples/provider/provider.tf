terraform {
  required_providers {
    autter = {
      source  = "Autter-dev/autter"
      version = "~> 0.1"
    }
  }
}

# The token is read from AUTTER_API_TOKEN when api_token is not set.
# Create it in the Autter dashboard under Settings → Access Tokens, with the
# api:read and api:write scopes.
provider "autter" {}
