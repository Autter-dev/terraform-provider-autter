# The import ID is "<repository>:<name>". Header values cannot be read back, so
# the first plan after an import updates the check once to send them.
terraform import autter_synthetic_check.health "acme/web:Health"
