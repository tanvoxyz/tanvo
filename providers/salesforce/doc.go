// Package salesforce is the Salesforce [integration.Gateway]. Salesforce
// receives nothing from Tanvo: the Tanvo package installed in the
// Salesforce org pulls customers, invoices and subscriptions through the
// API and listens to the webhooks, so a connection only records the
// org's instance ID (lago app/models/integrations/salesforce_integration.rb).
package salesforce
