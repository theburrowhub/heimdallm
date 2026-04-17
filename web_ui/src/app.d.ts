// See https://kit.svelte.dev/docs/types#app
declare global {
  namespace App {
    // interface Locals {}
    // interface PageData {}
    // interface PageState {}
    // interface Platform {}
    interface Error {
      message: string;
    }
  }
}

export {};
