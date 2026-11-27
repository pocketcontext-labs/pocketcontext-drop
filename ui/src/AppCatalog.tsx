import { deployRecipes, deployLink } from "./deployRecipes";

export default function AppCatalog() {
  return (
    <section className="panel wide" aria-labelledby="catalog-title">
      <h2 id="catalog-title">App catalog</h2>
      <ul className="apps">
        {deployRecipes.map(recipe => (
          <li className="row" key={recipe.id}>
            <div>
              <h3>{recipe.name}</h3>
              <p>{recipe.purpose}</p>
              <p className="quiet">{recipe.category} · {recipe.version} · <a href={recipe.projectUrl}>Upstream project</a></p>
            </div>
            <a className="button primary" href={deployLink(recipe)} aria-label={`Deploy ${recipe.name}`}>Deploy</a>
          </li>
        ))}
      </ul>
    </section>
  );
}
