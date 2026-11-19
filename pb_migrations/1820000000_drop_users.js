// Drop accounts: a role (admin or user), an app quota, suspension and who
// invited the account. Core creates accounts (the owner on --hosted-init, the
// local account on `drop init`, anyone else only when Hooks.AllowSignIn admits
// an address with an open invite); internal/dropserver/accounts.go sets these
// fields on every create and Drop's operations change them. Clients never write
// users records.
migrate((app) => {
  const users = app.findCollectionByNameOrId("users");
  // Every rule reading an account field is guarded by the collection name: a
  // token of another auth collection never matches.
  const signedIn = "@request.auth.id != '' && @request.auth.collectionName = 'users' && @request.auth.suspended = false";
  users.fields.add(new SelectField({name: "role", values: ["admin", "user"], maxSelect: 1, required: true}));
  users.fields.add(new NumberField({name: "app_quota", min: 0, max: 100, onlyInt: true}));
  users.fields.add(new BoolField({name: "suspended"}));
  users.fields.add(new RelationField({name: "invited_by", collectionId: users.id, maxSelect: 1, cascadeDelete: false}));
  // Admins list accounts with the user.list operation; the records API shows
  // an account only its own record (and users records are never a public
  // route of the hosted origin).
  users.listRule = null;
  users.viewRule = signedIn + " && id = @request.auth.id";
  users.createRule = null;
  users.updateRule = null;
  users.deleteRule = null;
  users.manageRule = null;
  // A suspended account cannot sign in or renew a session.
  users.authRule = "suspended = false";
  app.save(users);
  // No database with accounts has been released; a development database's
  // accounts become users, the local account an admin. The owner is made an
  // admin when the hosted server starts (accounts.go).
  app.db().newQuery("UPDATE users SET role = 'user', app_quota = 3 WHERE role = ''").execute();
  app.db().newQuery("UPDATE users SET role = 'admin' WHERE LOWER(email) = 'account@drop.invalid'").execute();
}, () => {
  throw new Error("Account policy rollback requires a deliberate backup restore.");
});
