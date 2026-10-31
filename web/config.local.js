// Local development: copy to config.js (gitignored). The deploy workflow
// writes config.js from the environment's Terraform outputs.
window.ACCOUNT_API = "http://localhost:7073";
window.PRODUCER_API = "http://localhost:7071";
window.CONSUMER_API = "http://localhost:7072";
window.TEST_MODE = true;
window.PACKS = { starter: { credits: 2000, price_eur: 10 }, pro: { credits: 12000, price_eur: 50 } };
window.PRICES_INCLUDE_VAT = true;
window.CONTACT_EMAIL = "support@example.com";
