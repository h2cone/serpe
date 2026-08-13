import { redirect } from "react-router";
import { api } from "~/lib/api";

export async function clientAction() {
  const created = await api.createSession();
  return redirect(`/sessions/${created.id}`, 303);
}
