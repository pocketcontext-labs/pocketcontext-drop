import { useState } from "react";
import type { Role } from "./dropApi";

export const accountRoles = ["user", "developer", "admin"] as const satisfies readonly Role[];

export function RoleOptions() {
  return accountRoles.map(role => <option key={role} value={role}>
    {role[0].toUpperCase() + role.slice(1)}
  </option>);
}

export function WaitlistInvitation({ disabled, label, onInvite }: {
  disabled: boolean;
  label: string;
  onInvite: (role: Role) => void;
}) {
  const [role, setRole] = useState<Role>("user");
  return <form className="directory-access" onSubmit={e => {
    e.preventDefault();
    onInvite(role);
  }}>
    <label>Role on approval
      <select value={role} disabled={disabled} onChange={e => setRole(e.target.value as Role)}>
        <RoleOptions />
      </select>
    </label>
    <button className="primary" disabled={disabled}>{label}</button>
  </form>;
}
