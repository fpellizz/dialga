import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

/** Opens the "Actions for <database>" menu of a row and returns its button. */
export async function openActions(database: string) {
  const button = screen.getByRole("button", { name: `Actions for ${database}` });
  await userEvent.click(button);
  await screen.findByRole("menu");
  return button;
}

/** Picks a menu item (by its accessible name) from the row's menu. */
export async function chooseAction(database: string, item: string | RegExp) {
  const button = await openActions(database);
  await userEvent.click(await screen.findByRole("menuitem", { name: item }));
  return button;
}
