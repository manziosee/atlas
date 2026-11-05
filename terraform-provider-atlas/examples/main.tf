terraform {
  required_providers {
    atlas = {
      source = "manziosee/atlas"
    }
  }
}

# Address and token can also come from ATLAS_ADDR and ATLAS_TOKEN.
provider "atlas" {
  address = "https://atlas.example.com:8080"
}

# A database that can't register itself: Terraform registers it and the
# owning Atlas node checks its health.
resource "atlas_service" "orders_db" {
  id           = "orders-db-primary"
  name         = "orders-db"
  address      = "10.0.4.20:5432"
  version      = "16.4"
  tags         = ["primary", "postgres"]
  health_check = "tcp://10.0.4.20:5432"
}

# Feed every healthy payments instance into something else, for example a
# load balancer's target list.
data "atlas_services" "payments" {
  name   = "payments"
  health = "healthy"
}

output "payments_addresses" {
  value = data.atlas_services.payments.addresses
}

# The instance a client in eu would be routed to.
data "atlas_lookup" "payments_eu" {
  name   = "payments"
  region = "eu"
}

output "payments_eu" {
  value = data.atlas_lookup.payments_eu.address
}
