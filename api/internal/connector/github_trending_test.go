package connector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// ghTrendingFixture, github.com/trending?since=daily canlı sayfasından
// (2026-09-04) kırpılmış GERÇEK 2 <article class="Box-row"> bloğu — uydurma
// HTML değil. Sınıf adları, stargazers linki, "N stars today" biçimi ve
// binlik virgüllü sayılar canlı yapıyla birebir.
const ghTrendingFixture = `<!DOCTYPE html>
<html><body>
<div class="Box">
<article class="Box-row">
  <div class="float-right d-flex">
      <a href="/sponsors/mattpocock" aria-label="Sponsor @mattpocock" data-hydro-click="{&quot;event_type&quot;:&quot;sponsors.button_click&quot;,&quot;payload&quot;:{&quot;button&quot;:&quot;TRENDING_REPO_SPONSOR&quot;,&quot;sponsorable_login&quot;:&quot;mattpocock&quot;,&quot;originating_url&quot;:&quot;https://github.com/trending?since=daily&quot;,&quot;user_id&quot;:null}}" data-hydro-click-hmac="f92fa0d458704aad92920b9b4d36abbaa1e66e7d3e3dd429d60bf7e323e0e447" data-view-component="true" class="Button--secondary Button--small Button mr-2 tmp-mr-2">  <span class="Button-content">
    <span class="Button-label"><svg aria-hidden="true" data-component="Octicon" height="16" viewBox="0 0 16 16" version="1.1" width="16" data-view-component="true" class="octicon octicon-heart icon-sponsor mr-0 tmp-mr-0 mr-md-1 tmp-mr-md-1 v-align-middle color-fg-sponsors anim-pulse-in">
    <path d="m8 14.25.345.666a.75.75 0 0 1-.69 0l-.008-.004-.018-.01a7.152 7.152 0 0 1-.31-.17 22.055 22.055 0 0 1-3.434-2.414C2.045 10.731 0 8.35 0 5.5 0 2.836 2.086 1 4.25 1 5.797 1 7.153 1.802 8 3.02 8.847 1.802 10.203 1 11.75 1 13.914 1 16 2.836 16 5.5c0 2.85-2.045 5.231-3.885 6.818a22.066 22.066 0 0 1-3.744 2.584l-.018.01-.006.003h-.002ZM4.25 2.5c-1.336 0-2.75 1.164-2.75 3 0 2.15 1.58 4.144 3.365 5.682A20.58 20.58 0 0 0 8 13.393a20.58 20.58 0 0 0 3.135-2.211C12.92 9.644 14.5 7.65 14.5 5.5c0-1.836-1.414-3-2.75-3-1.373 0-2.609.986-3.029 2.456a.749.749 0 0 1-1.442 0C6.859 3.486 5.623 2.5 4.25 2.5Z"></path>
</svg>
    <span class="d-none d-md-inline v-align-middle" >
      Sponsor
    </span></span>
  </span>
</a>


      <div data-view-component="true" class="BtnGroup d-flex">
        <a href="/login?return_to=%2Fmattpocock%2Fskills" rel="nofollow" data-hydro-click="{&quot;event_type&quot;:&quot;authentication.click&quot;,&quot;payload&quot;:{&quot;location_in_page&quot;:&quot;star button&quot;,&quot;repository_id&quot;:1148788086,&quot;auth_type&quot;:&quot;LOG_IN&quot;,&quot;originating_url&quot;:&quot;https://github.com/trending?since=daily&quot;,&quot;user_id&quot;:null}}" data-hydro-click-hmac="f312fb66bbefedc243856e719ad99f591d36b7c3c4f203d9431089fa89a9f3e6" aria-label="You must be signed in to star a repository" data-view-component="true" class="tooltipped tooltipped-sw btn-sm btn">    <svg aria-hidden="true" data-component="Octicon" height="16" viewBox="0 0 16 16" version="1.1" width="16" data-view-component="true" class="octicon octicon-star v-align-text-bottom d-none d-md-inline-block mr-2 tmp-mr-2">
    <path d="M8 .25a.75.75 0 0 1 .673.418l1.882 3.815 4.21.612a.75.75 0 0 1 .416 1.279l-3.046 2.97.719 4.192a.751.751 0 0 1-1.088.791L8 12.347l-3.766 1.98a.75.75 0 0 1-1.088-.79l.72-4.194L.818 6.374a.75.75 0 0 1 .416-1.28l4.21-.611L7.327.668A.75.75 0 0 1 8 .25Zm0 2.445L6.615 5.5a.75.75 0 0 1-.564.41l-3.097.45 2.24 2.184a.75.75 0 0 1 .216.664l-.528 3.084 2.769-1.456a.75.75 0 0 1 .698 0l2.77 1.456-.53-3.084a.75.75 0 0 1 .216-.664l2.24-2.183-3.096-.45a.75.75 0 0 1-.564-.41L8 2.694Z"></path>
</svg><svg aria-hidden="true" data-component="Octicon" height="16" viewBox="0 0 16 16" version="1.1" width="16" data-view-component="true" class="octicon octicon-star mr-0 tmp-mr-0 v-align-text-bottom d-inline-block d-md-none">
    <path d="M8 .25a.75.75 0 0 1 .673.418l1.882 3.815 4.21.612a.75.75 0 0 1 .416 1.279l-3.046 2.97.719 4.192a.751.751 0 0 1-1.088.791L8 12.347l-3.766 1.98a.75.75 0 0 1-1.088-.79l.72-4.194L.818 6.374a.75.75 0 0 1 .416-1.28l4.21-.611L7.327.668A.75.75 0 0 1 8 .25Zm0 2.445L6.615 5.5a.75.75 0 0 1-.564.41l-3.097.45 2.24 2.184a.75.75 0 0 1 .216.664l-.528 3.084 2.769-1.456a.75.75 0 0 1 .698 0l2.77 1.456-.53-3.084a.75.75 0 0 1 .216-.664l2.24-2.183-3.096-.45a.75.75 0 0 1-.564-.41L8 2.694Z"></path>
</svg>
        <span data-view-component="true" class="d-none d-md-inline">
          Star
</span>
</a></div>
  </div>

  <h2 class="h3 lh-condensed">
    <a data-hydro-click="{&quot;event_type&quot;:&quot;explore.click&quot;,&quot;payload&quot;:{&quot;click_context&quot;:&quot;TRENDING_REPOSITORIES_PAGE&quot;,&quot;click_target&quot;:&quot;REPOSITORY&quot;,&quot;click_visual_representation&quot;:&quot;REPOSITORY_NAME_HEADING&quot;,&quot;actor_id&quot;:null,&quot;record_id&quot;:1148788086,&quot;originating_url&quot;:&quot;https://github.com/trending?since=daily&quot;,&quot;user_id&quot;:null}}" data-hydro-click-hmac="736b93fe5b6632513715a3d0ce059d87797e91fd0d74b0f27fbe00ca746f5cb8" href="/mattpocock/skills" data-view-component="true" class="Link"><svg aria-hidden="true" data-component="Octicon" height="16" viewBox="0 0 16 16" version="1.1" width="16" data-view-component="true" class="octicon octicon-repo mr-1 tmp-mr-1 color-fg-muted">
    <path d="M2 2.5A2.5 2.5 0 0 1 4.5 0h8.75a.75.75 0 0 1 .75.75v12.5a.75.75 0 0 1-.75.75h-2.5a.75.75 0 0 1 0-1.5h1.75v-2h-8a1 1 0 0 0-.714 1.7.75.75 0 1 1-1.072 1.05A2.495 2.495 0 0 1 2 11.5Zm10.5-1h-8a1 1 0 0 0-1 1v6.708A2.486 2.486 0 0 1 4.5 9h8ZM5 12.25a.25.25 0 0 1 .25-.25h3.5a.25.25 0 0 1 .25.25v3.25a.25.25 0 0 1-.4.2l-1.45-1.087a.249.249 0 0 0-.3 0L5.4 15.7a.25.25 0 0 1-.4-.2Z"></path>
</svg>

      <span data-view-component="true" class="text-normal">
        mattpocock /
</span>
      skills</a>  </h2>

    <p class="col-9 color-fg-muted my-1 tmp-pr-4">
      Skills for Real Engineers. Straight from my .agents directory.
    </p>

  <div class="f6 color-fg-muted mt-2">
      <span class="tmp-mr-3 d-inline-block ml-0 tmp-ml-0">
  <span class="repo-language-color" style="background-color: #89e051"></span>
  <span itemprop="programmingLanguage">Shell</span>
</span>


      <a href="/mattpocock/skills/stargazers" data-view-component="true" class="tmp-mr-3 Link Link--muted d-inline-block"><svg aria-label="star" role="img" data-component="Octicon" height="16" viewBox="0 0 16 16" version="1.1" width="16" data-view-component="true" class="octicon octicon-star">
    <path d="M8 .25a.75.75 0 0 1 .673.418l1.882 3.815 4.21.612a.75.75 0 0 1 .416 1.279l-3.046 2.97.719 4.192a.751.751 0 0 1-1.088.791L8 12.347l-3.766 1.98a.75.75 0 0 1-1.088-.79l.72-4.194L.818 6.374a.75.75 0 0 1 .416-1.28l4.21-.611L7.327.668A.75.75 0 0 1 8 .25Zm0 2.445L6.615 5.5a.75.75 0 0 1-.564.41l-3.097.45 2.24 2.184a.75.75 0 0 1 .216.664l-.528 3.084 2.769-1.456a.75.75 0 0 1 .698 0l2.77 1.456-.53-3.084a.75.75 0 0 1 .216-.664l2.24-2.183-3.096-.45a.75.75 0 0 1-.564-.41L8 2.694Z"></path>
</svg>
        248,641</a>
      <a href="/mattpocock/skills/forks" data-view-component="true" class="tmp-mr-3 Link Link--muted d-inline-block"><svg aria-label="fork" role="img" data-component="Octicon" height="16" viewBox="0 0 16 16" version="1.1" width="16" data-view-component="true" class="octicon octicon-repo-forked">
    <path d="M5 5.372v.878c0 .414.336.75.75.75h4.5a.75.75 0 0 0 .75-.75v-.878a2.25 2.25 0 1 1 1.5 0v.878a2.25 2.25 0 0 1-2.25 2.25h-1.5v2.128a2.251 2.251 0 1 1-1.5 0V8.5h-1.5A2.25 2.25 0 0 1 3.5 6.25v-.878a2.25 2.25 0 1 1 1.5 0ZM5 3.25a.75.75 0 1 0-1.5 0 .75.75 0 0 0 1.5 0Zm6.75.75a.75.75 0 1 0 0-1.5.75.75 0 0 0 0 1.5Zm-3 8.75a.75.75 0 1 0-1.5 0 .75.75 0 0 0 1.5 0Z"></path>
</svg>
        21,055</a>
      <span data-view-component="true" class="tmp-mr-3 d-inline-block">
        Built by

          <a class="d-inline-block" data-hydro-click="{&quot;event_type&quot;:&quot;explore.click&quot;,&quot;payload&quot;:{&quot;click_context&quot;:&quot;TRENDING_REPOSITORIES_PAGE&quot;,&quot;click_target&quot;:&quot;CONTRIBUTING_DEVELOPER&quot;,&quot;click_visual_representation&quot;:&quot;DEVELOPER_AVATAR&quot;,&quot;actor_id&quot;:null,&quot;record_id&quot;:null,&quot;originating_url&quot;:&quot;https://github.com/trending&quot;,&quot;user_id&quot;:10896188}}" data-hydro-click-hmac="8b6b5f9b57321d2b6ef916c0d2fa53b43787d31ca106032a35cf510e00824474" data-hovercard-type="user" data-hovercard-url="/users/mattpocock/hovercard" data-octo-click="hovercard-link-click" data-octo-dimensions="link_type:self" href="/mattpocock"><img class="avatar mb-1 avatar-user" src="https://avatars.githubusercontent.com/u/28293365?s=40&amp;v=4" width="20" height="20" alt="@mattpocock" /></a>
          <a class="d-inline-block" data-hydro-click="{&quot;event_type&quot;:&quot;explore.click&quot;,&quot;payload&quot;:{&quot;click_context&quot;:&quot;TRENDING_REPOSITORIES_PAGE&quot;,&quot;click_target&quot;:&quot;CONTRIBUTING_DEVELOPER&quot;,&quot;click_visual_representation&quot;:&quot;DEVELOPER_AVATAR&quot;,&quot;actor_id&quot;:null,&quot;record_id&quot;:null,&quot;originating_url&quot;:&quot;https://github.com/trending&quot;,&quot;user_id&quot;:10896188}}" data-hydro-click-hmac="8b6b5f9b57321d2b6ef916c0d2fa53b43787d31ca106032a35cf510e00824474" data-hovercard-type="user" data-hovercard-url="/users/claude/hovercard" data-octo-click="hovercard-link-click" data-octo-dimensions="link_type:self" href="/claude"><img class="avatar mb-1 avatar-user" src="https://avatars.githubusercontent.com/u/81847?s=40&amp;v=4" width="20" height="20" alt="@claude" /></a>
          <a class="d-inline-block" data-hydro-click="{&quot;event_type&quot;:&quot;explore.click&quot;,&quot;payload&quot;:{&quot;click_context&quot;:&quot;TRENDING_REPOSITORIES_PAGE&quot;,&quot;click_target&quot;:&quot;CONTRIBUTING_DEVELOPER&quot;,&quot;click_visual_representation&quot;:&quot;DEVELOPER_AVATAR&quot;,&quot;actor_id&quot;:null,&quot;record_id&quot;:null,&quot;originating_url&quot;:&quot;https://github.com/trending&quot;,&quot;user_id&quot;:10896188}}" data-hydro-click-hmac="8b6b5f9b57321d2b6ef916c0d2fa53b43787d31ca106032a35cf510e00824474" href="/apps/github-actions"><img class="avatar mb-1" src="https://avatars.githubusercontent.com/in/15368?s=40&amp;v=4" width="20" height="20" alt="@github-actions" /></a>
          <a class="d-inline-block" data-hydro-click="{&quot;event_type&quot;:&quot;explore.click&quot;,&quot;payload&quot;:{&quot;click_context&quot;:&quot;TRENDING_REPOSITORIES_PAGE&quot;,&quot;click_target&quot;:&quot;CONTRIBUTING_DEVELOPER&quot;,&quot;click_visual_representation&quot;:&quot;DEVELOPER_AVATAR&quot;,&quot;actor_id&quot;:null,&quot;record_id&quot;:null,&quot;originating_url&quot;:&quot;https://github.com/trending&quot;,&quot;user_id&quot;:10896188}}" data-hydro-click-hmac="8b6b5f9b57321d2b6ef916c0d2fa53b43787d31ca106032a35cf510e00824474" data-hovercard-type="user" data-hovercard-url="/users/gabimoncha/hovercard" data-octo-click="hovercard-link-click" data-octo-dimensions="link_type:self" href="/gabimoncha"><img class="avatar mb-1 avatar-user" src="https://avatars.githubusercontent.com/u/39256258?s=40&amp;v=4" width="20" height="20" alt="@gabimoncha" /></a>
</span>
      <span data-view-component="true" class="d-inline-block float-sm-right">
        <svg aria-hidden="true" data-component="Octicon" height="16" viewBox="0 0 16 16" version="1.1" width="16" data-view-component="true" class="octicon octicon-star">
    <path d="M8 .25a.75.75 0 0 1 .673.418l1.882 3.815 4.21.612a.75.75 0 0 1 .416 1.279l-3.046 2.97.719 4.192a.751.751 0 0 1-1.088.791L8 12.347l-3.766 1.98a.75.75 0 0 1-1.088-.79l.72-4.194L.818 6.374a.75.75 0 0 1 .416-1.28l4.21-.611L7.327.668A.75.75 0 0 1 8 .25Zm0 2.445L6.615 5.5a.75.75 0 0 1-.564.41l-3.097.45 2.24 2.184a.75.75 0 0 1 .216.664l-.528 3.084 2.769-1.456a.75.75 0 0 1 .698 0l2.77 1.456-.53-3.084a.75.75 0 0 1 .216-.664l2.24-2.183-3.096-.45a.75.75 0 0 1-.564-.41L8 2.694Z"></path>
</svg>
        1,601 stars today
</span>  </div>
</article>
<article class="Box-row">
  <div class="float-right d-flex">
      <a href="/sponsors/DietrichGebert" aria-label="Sponsor @DietrichGebert" data-hydro-click="{&quot;event_type&quot;:&quot;sponsors.button_click&quot;,&quot;payload&quot;:{&quot;button&quot;:&quot;TRENDING_REPO_SPONSOR&quot;,&quot;sponsorable_login&quot;:&quot;DietrichGebert&quot;,&quot;originating_url&quot;:&quot;https://github.com/trending?since=daily&quot;,&quot;user_id&quot;:null}}" data-hydro-click-hmac="ddafc85e17b17110939fa0ee6552417a02f001741ebaae6c8f9c2f58da927c95" data-view-component="true" class="Button--secondary Button--small Button mr-2 tmp-mr-2">  <span class="Button-content">
    <span class="Button-label"><svg aria-hidden="true" data-component="Octicon" height="16" viewBox="0 0 16 16" version="1.1" width="16" data-view-component="true" class="octicon octicon-heart icon-sponsor mr-0 tmp-mr-0 mr-md-1 tmp-mr-md-1 v-align-middle color-fg-sponsors anim-pulse-in">
    <path d="m8 14.25.345.666a.75.75 0 0 1-.69 0l-.008-.004-.018-.01a7.152 7.152 0 0 1-.31-.17 22.055 22.055 0 0 1-3.434-2.414C2.045 10.731 0 8.35 0 5.5 0 2.836 2.086 1 4.25 1 5.797 1 7.153 1.802 8 3.02 8.847 1.802 10.203 1 11.75 1 13.914 1 16 2.836 16 5.5c0 2.85-2.045 5.231-3.885 6.818a22.066 22.066 0 0 1-3.744 2.584l-.018.01-.006.003h-.002ZM4.25 2.5c-1.336 0-2.75 1.164-2.75 3 0 2.15 1.58 4.144 3.365 5.682A20.58 20.58 0 0 0 8 13.393a20.58 20.58 0 0 0 3.135-2.211C12.92 9.644 14.5 7.65 14.5 5.5c0-1.836-1.414-3-2.75-3-1.373 0-2.609.986-3.029 2.456a.749.749 0 0 1-1.442 0C6.859 3.486 5.623 2.5 4.25 2.5Z"></path>
</svg>
    <span class="d-none d-md-inline v-align-middle" >
      Sponsor
    </span></span>
  </span>
</a>


      <div data-view-component="true" class="BtnGroup d-flex">
        <a href="/login?return_to=%2FDietrichGebert%2Fponytail" rel="nofollow" data-hydro-click="{&quot;event_type&quot;:&quot;authentication.click&quot;,&quot;payload&quot;:{&quot;location_in_page&quot;:&quot;star button&quot;,&quot;repository_id&quot;:1266797999,&quot;auth_type&quot;:&quot;LOG_IN&quot;,&quot;originating_url&quot;:&quot;https://github.com/trending?since=daily&quot;,&quot;user_id&quot;:null}}" data-hydro-click-hmac="2f491c50704e28130624accb0f6489e683fc1a03c8657fc634465694e4dab9d2" aria-label="You must be signed in to star a repository" data-view-component="true" class="tooltipped tooltipped-sw btn-sm btn">    <svg aria-hidden="true" data-component="Octicon" height="16" viewBox="0 0 16 16" version="1.1" width="16" data-view-component="true" class="octicon octicon-star v-align-text-bottom d-none d-md-inline-block mr-2 tmp-mr-2">
    <path d="M8 .25a.75.75 0 0 1 .673.418l1.882 3.815 4.21.612a.75.75 0 0 1 .416 1.279l-3.046 2.97.719 4.192a.751.751 0 0 1-1.088.791L8 12.347l-3.766 1.98a.75.75 0 0 1-1.088-.79l.72-4.194L.818 6.374a.75.75 0 0 1 .416-1.28l4.21-.611L7.327.668A.75.75 0 0 1 8 .25Zm0 2.445L6.615 5.5a.75.75 0 0 1-.564.41l-3.097.45 2.24 2.184a.75.75 0 0 1 .216.664l-.528 3.084 2.769-1.456a.75.75 0 0 1 .698 0l2.77 1.456-.53-3.084a.75.75 0 0 1 .216-.664l2.24-2.183-3.096-.45a.75.75 0 0 1-.564-.41L8 2.694Z"></path>
</svg><svg aria-hidden="true" data-component="Octicon" height="16" viewBox="0 0 16 16" version="1.1" width="16" data-view-component="true" class="octicon octicon-star mr-0 tmp-mr-0 v-align-text-bottom d-inline-block d-md-none">
    <path d="M8 .25a.75.75 0 0 1 .673.418l1.882 3.815 4.21.612a.75.75 0 0 1 .416 1.279l-3.046 2.97.719 4.192a.751.751 0 0 1-1.088.791L8 12.347l-3.766 1.98a.75.75 0 0 1-1.088-.79l.72-4.194L.818 6.374a.75.75 0 0 1 .416-1.28l4.21-.611L7.327.668A.75.75 0 0 1 8 .25Zm0 2.445L6.615 5.5a.75.75 0 0 1-.564.41l-3.097.45 2.24 2.184a.75.75 0 0 1 .216.664l-.528 3.084 2.769-1.456a.75.75 0 0 1 .698 0l2.77 1.456-.53-3.084a.75.75 0 0 1 .216-.664l2.24-2.183-3.096-.45a.75.75 0 0 1-.564-.41L8 2.694Z"></path>
</svg>
        <span data-view-component="true" class="d-none d-md-inline">
          Star
</span>
</a></div>
  </div>

  <h2 class="h3 lh-condensed">
    <a data-hydro-click="{&quot;event_type&quot;:&quot;explore.click&quot;,&quot;payload&quot;:{&quot;click_context&quot;:&quot;TRENDING_REPOSITORIES_PAGE&quot;,&quot;click_target&quot;:&quot;REPOSITORY&quot;,&quot;click_visual_representation&quot;:&quot;REPOSITORY_NAME_HEADING&quot;,&quot;actor_id&quot;:null,&quot;record_id&quot;:1266797999,&quot;originating_url&quot;:&quot;https://github.com/trending?since=daily&quot;,&quot;user_id&quot;:null}}" data-hydro-click-hmac="a51ce123eae80acdfc2d0a119324ad0c30237bf5453aa3aa44b63ea2d30c9063" href="/DietrichGebert/ponytail" data-view-component="true" class="Link"><svg aria-hidden="true" data-component="Octicon" height="16" viewBox="0 0 16 16" version="1.1" width="16" data-view-component="true" class="octicon octicon-repo mr-1 tmp-mr-1 color-fg-muted">
    <path d="M2 2.5A2.5 2.5 0 0 1 4.5 0h8.75a.75.75 0 0 1 .75.75v12.5a.75.75 0 0 1-.75.75h-2.5a.75.75 0 0 1 0-1.5h1.75v-2h-8a1 1 0 0 0-.714 1.7.75.75 0 1 1-1.072 1.05A2.495 2.495 0 0 1 2 11.5Zm10.5-1h-8a1 1 0 0 0-1 1v6.708A2.486 2.486 0 0 1 4.5 9h8ZM5 12.25a.25.25 0 0 1 .25-.25h3.5a.25.25 0 0 1 .25.25v3.25a.25.25 0 0 1-.4.2l-1.45-1.087a.249.249 0 0 0-.3 0L5.4 15.7a.25.25 0 0 1-.4-.2Z"></path>
</svg>

      <span data-view-component="true" class="text-normal">
        DietrichGebert /
</span>
      ponytail</a>  </h2>

    <p class="col-9 color-fg-muted my-1 tmp-pr-4">
      Makes your AI agent think like the laziest senior dev in the room. The best code is the code you never wrote.
    </p>

  <div class="f6 color-fg-muted mt-2">
      <span class="tmp-mr-3 d-inline-block ml-0 tmp-ml-0">
  <span class="repo-language-color" style="background-color: #f1e05a"></span>
  <span itemprop="programmingLanguage">JavaScript</span>
</span>


      <a href="/DietrichGebert/ponytail/stargazers" data-view-component="true" class="tmp-mr-3 Link Link--muted d-inline-block"><svg aria-label="star" role="img" data-component="Octicon" height="16" viewBox="0 0 16 16" version="1.1" width="16" data-view-component="true" class="octicon octicon-star">
    <path d="M8 .25a.75.75 0 0 1 .673.418l1.882 3.815 4.21.612a.75.75 0 0 1 .416 1.279l-3.046 2.97.719 4.192a.751.751 0 0 1-1.088.791L8 12.347l-3.766 1.98a.75.75 0 0 1-1.088-.79l.72-4.194L.818 6.374a.75.75 0 0 1 .416-1.28l4.21-.611L7.327.668A.75.75 0 0 1 8 .25Zm0 2.445L6.615 5.5a.75.75 0 0 1-.564.41l-3.097.45 2.24 2.184a.75.75 0 0 1 .216.664l-.528 3.084 2.769-1.456a.75.75 0 0 1 .698 0l2.77 1.456-.53-3.084a.75.75 0 0 1 .216-.664l2.24-2.183-3.096-.45a.75.75 0 0 1-.564-.41L8 2.694Z"></path>
</svg>
        124,209</a>
      <a href="/DietrichGebert/ponytail/forks" data-view-component="true" class="tmp-mr-3 Link Link--muted d-inline-block"><svg aria-label="fork" role="img" data-component="Octicon" height="16" viewBox="0 0 16 16" version="1.1" width="16" data-view-component="true" class="octicon octicon-repo-forked">
    <path d="M5 5.372v.878c0 .414.336.75.75.75h4.5a.75.75 0 0 0 .75-.75v-.878a2.25 2.25 0 1 1 1.5 0v.878a2.25 2.25 0 0 1-2.25 2.25h-1.5v2.128a2.251 2.251 0 1 1-1.5 0V8.5h-1.5A2.25 2.25 0 0 1 3.5 6.25v-.878a2.25 2.25 0 1 1 1.5 0ZM5 3.25a.75.75 0 1 0-1.5 0 .75.75 0 0 0 1.5 0Zm6.75.75a.75.75 0 1 0 0-1.5.75.75 0 0 0 0 1.5Zm-3 8.75a.75.75 0 1 0-1.5 0 .75.75 0 0 0 1.5 0Z"></path>
</svg>
        6,701</a>
      <span data-view-component="true" class="tmp-mr-3 d-inline-block">
        Built by

          <a class="d-inline-block" data-hydro-click="{&quot;event_type&quot;:&quot;explore.click&quot;,&quot;payload&quot;:{&quot;click_context&quot;:&quot;TRENDING_REPOSITORIES_PAGE&quot;,&quot;click_target&quot;:&quot;CONTRIBUTING_DEVELOPER&quot;,&quot;click_visual_representation&quot;:&quot;DEVELOPER_AVATAR&quot;,&quot;actor_id&quot;:null,&quot;record_id&quot;:null,&quot;originating_url&quot;:&quot;https://github.com/trending&quot;,&quot;user_id&quot;:10896188}}" data-hydro-click-hmac="8b6b5f9b57321d2b6ef916c0d2fa53b43787d31ca106032a35cf510e00824474" data-hovercard-type="user" data-hovercard-url="/users/DietrichGebert/hovercard" data-octo-click="hovercard-link-click" data-octo-dimensions="link_type:self" href="/DietrichGebert"><img class="avatar mb-1 avatar-user" src="https://avatars.githubusercontent.com/u/137048761?s=40&amp;v=4" width="20" height="20" alt="@DietrichGebert" /></a>
          <a class="d-inline-block" data-hydro-click="{&quot;event_type&quot;:&quot;explore.click&quot;,&quot;payload&quot;:{&quot;click_context&quot;:&quot;TRENDING_REPOSITORIES_PAGE&quot;,&quot;click_target&quot;:&quot;CONTRIBUTING_DEVELOPER&quot;,&quot;click_visual_representation&quot;:&quot;DEVELOPER_AVATAR&quot;,&quot;actor_id&quot;:null,&quot;record_id&quot;:null,&quot;originating_url&quot;:&quot;https://github.com/trending&quot;,&quot;user_id&quot;:10896188}}" data-hydro-click-hmac="8b6b5f9b57321d2b6ef916c0d2fa53b43787d31ca106032a35cf510e00824474" data-hovercard-type="user" data-hovercard-url="/users/claude/hovercard" data-octo-click="hovercard-link-click" data-octo-dimensions="link_type:self" href="/claude"><img class="avatar mb-1 avatar-user" src="https://avatars.githubusercontent.com/u/81847?s=40&amp;v=4" width="20" height="20" alt="@claude" /></a>
          <a class="d-inline-block" data-hydro-click="{&quot;event_type&quot;:&quot;explore.click&quot;,&quot;payload&quot;:{&quot;click_context&quot;:&quot;TRENDING_REPOSITORIES_PAGE&quot;,&quot;click_target&quot;:&quot;CONTRIBUTING_DEVELOPER&quot;,&quot;click_visual_representation&quot;:&quot;DEVELOPER_AVATAR&quot;,&quot;actor_id&quot;:null,&quot;record_id&quot;:null,&quot;originating_url&quot;:&quot;https://github.com/trending&quot;,&quot;user_id&quot;:10896188}}" data-hydro-click-hmac="8b6b5f9b57321d2b6ef916c0d2fa53b43787d31ca106032a35cf510e00824474" data-hovercard-type="user" data-hovercard-url="/users/Lakshya77089/hovercard" data-octo-click="hovercard-link-click" data-octo-dimensions="link_type:self" href="/Lakshya77089"><img class="avatar mb-1 avatar-user" src="https://avatars.githubusercontent.com/u/169168095?s=40&amp;v=4" width="20" height="20" alt="@Lakshya77089" /></a>
          <a class="d-inline-block" data-hydro-click="{&quot;event_type&quot;:&quot;explore.click&quot;,&quot;payload&quot;:{&quot;click_context&quot;:&quot;TRENDING_REPOSITORIES_PAGE&quot;,&quot;click_target&quot;:&quot;CONTRIBUTING_DEVELOPER&quot;,&quot;click_visual_representation&quot;:&quot;DEVELOPER_AVATAR&quot;,&quot;actor_id&quot;:null,&quot;record_id&quot;:null,&quot;originating_url&quot;:&quot;https://github.com/trending&quot;,&quot;user_id&quot;:10896188}}" data-hydro-click-hmac="8b6b5f9b57321d2b6ef916c0d2fa53b43787d31ca106032a35cf510e00824474" data-hovercard-type="user" data-hovercard-url="/users/ousamabenyounes/hovercard" data-octo-click="hovercard-link-click" data-octo-dimensions="link_type:self" href="/ousamabenyounes"><img class="avatar mb-1 avatar-user" src="https://avatars.githubusercontent.com/u/2910651?s=40&amp;v=4" width="20" height="20" alt="@ousamabenyounes" /></a>
          <a class="d-inline-block" data-hydro-click="{&quot;event_type&quot;:&quot;explore.click&quot;,&quot;payload&quot;:{&quot;click_context&quot;:&quot;TRENDING_REPOSITORIES_PAGE&quot;,&quot;click_target&quot;:&quot;CONTRIBUTING_DEVELOPER&quot;,&quot;click_visual_representation&quot;:&quot;DEVELOPER_AVATAR&quot;,&quot;actor_id&quot;:null,&quot;record_id&quot;:null,&quot;originating_url&quot;:&quot;https://github.com/trending&quot;,&quot;user_id&quot;:10896188}}" data-hydro-click-hmac="8b6b5f9b57321d2b6ef916c0d2fa53b43787d31ca106032a35cf510e00824474" data-hovercard-type="user" data-hovercard-url="/users/dhedhialy/hovercard" data-octo-click="hovercard-link-click" data-octo-dimensions="link_type:self" href="/dhedhialy"><img class="avatar mb-1 avatar-user" src="https://avatars.githubusercontent.com/u/91044156?s=40&amp;v=4" width="20" height="20" alt="@dhedhialy" /></a>
</span>
      <span data-view-component="true" class="d-inline-block float-sm-right">
        <svg aria-hidden="true" data-component="Octicon" height="16" viewBox="0 0 16 16" version="1.1" width="16" data-view-component="true" class="octicon octicon-star">
    <path d="M8 .25a.75.75 0 0 1 .673.418l1.882 3.815 4.21.612a.75.75 0 0 1 .416 1.279l-3.046 2.97.719 4.192a.751.751 0 0 1-1.088.791L8 12.347l-3.766 1.98a.75.75 0 0 1-1.088-.79l.72-4.194L.818 6.374a.75.75 0 0 1 .416-1.28l4.21-.611L7.327.668A.75.75 0 0 1 8 .25Zm0 2.445L6.615 5.5a.75.75 0 0 1-.564.41l-3.097.45 2.24 2.184a.75.75 0 0 1 .216.664l-.528 3.084 2.769-1.456a.75.75 0 0 1 .698 0l2.77 1.456-.53-3.084a.75.75 0 0 1 .216-.664l2.24-2.183-3.096-.45a.75.75 0 0 1-.564-.41L8 2.694Z"></path>
</svg>
        2,128 stars today
</span>  </div>
</article>
</div>
</body></html>
`

func TestGitHubTrendingFetchNew(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(ghTrendingFixture))
	}))
	defer srv.Close()

	gh := &GitHubTrending{BaseURL: srv.URL}
	src := store.Source{Platform: "github_trending", Community: "daily"}

	posts, newRef, err := gh.FetchNew(context.Background(), src)
	if err != nil {
		t.Fatalf("FetchNew: %v", err)
	}
	if gotPath != "/trending" || gotQuery != "since=daily" {
		t.Errorf("yanlış istek: path=%q query=%q", gotPath, gotQuery)
	}
	if newRef != "" {
		t.Errorf("last_seen_ref kullanılmamalı, geldi: %q", newRef)
	}
	if len(posts) != 2 {
		t.Fatalf("2 repo beklenirdi, geldi: %d", len(posts))
	}

	today := time.Now().UTC().Truncate(24 * time.Hour).Format("2006-01-02")

	p0 := posts[0]
	if p0.Title != "mattpocock/skills" {
		t.Errorf("yanlış Title: %q", p0.Title)
	}
	if p0.SourceRef != "mattpocock/skills:"+today {
		t.Errorf("yanlış SourceRef: %q", p0.SourceRef)
	}
	if p0.Score != 1601 {
		t.Errorf("yanlış Score (günlük artış): %d", p0.Score)
	}
	if p0.URL != srv.URL+"/mattpocock/skills" {
		t.Errorf("yanlış URL: %q", p0.URL)
	}
	if p0.Author != "mattpocock" {
		t.Errorf("yanlış Author: %q", p0.Author)
	}
	wantBody := "★248,641 · Skills for Real Engineers. Straight from my .agents directory."
	if p0.Body != wantBody {
		t.Errorf("yanlış Body: %q, beklenen: %q", p0.Body, wantBody)
	}
	if p0.Community != "daily" {
		t.Errorf("yanlış Community: %q", p0.Community)
	}

	p1 := posts[1]
	if p1.Title != "DietrichGebert/ponytail" {
		t.Errorf("yanlış Title: %q", p1.Title)
	}
	if p1.Score != 2128 {
		t.Errorf("yanlış Score (günlük artış): %d", p1.Score)
	}
	wantBody1 := "★124,209 · Makes your AI agent think like the laziest senior dev in the room. The best code is the code you never wrote."
	if p1.Body != wantBody1 {
		t.Errorf("yanlış Body: %q, beklenen: %q", p1.Body, wantBody1)
	}
}

func TestGitHubTrendingZeroArticlesIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>yapı değişmiş, hiç <article> yok</body></html>"))
	}))
	defer srv.Close()

	gh := &GitHubTrending{BaseURL: srv.URL}
	src := store.Source{Platform: "github_trending", Community: "daily"}

	posts, _, err := gh.FetchNew(context.Background(), src)
	if err == nil {
		t.Fatal("0 article parse edilince hata beklenirdi, nil döndü")
	}
	if !strings.Contains(err.Error(), "0 repo parse edildi") {
		t.Errorf("hata mesajı beklenen ipucunu taşımıyor: %v", err)
	}
	if posts != nil {
		t.Errorf("hata durumunda posts nil olmalı, geldi: %+v", posts)
	}
}

func TestGitHubTrendingHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("forbidden"))
	}))
	defer srv.Close()

	gh := &GitHubTrending{BaseURL: srv.URL}
	src := store.Source{Platform: "github_trending", Community: "daily"}

	if _, _, err := gh.FetchNew(context.Background(), src); err == nil {
		t.Fatal("HTTP 403 hata döndürmeliydi")
	}
}

// --- FetchRepoMeta (#89 ivme kapısı madde 2-3) ---

func TestFetchRepoMetaOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/foo/bar" {
			t.Errorf("yanlış path: %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"created_at":"2026-03-01T00:00:00Z","stargazers_count":500,"forks_count":50,"open_issues_count":12}`))
	}))
	defer srv.Close()

	meta, err := fetchRepoMeta(context.Background(), srv.URL, "foo", "bar")
	if err != nil {
		t.Fatalf("fetchRepoMeta: %v", err)
	}
	if meta.StargazersCount != 500 || meta.ForksCount != 50 || meta.OpenIssuesCount != 12 {
		t.Errorf("yanlış meta: %+v", meta)
	}
	wantCreated, _ := time.Parse(time.RFC3339, "2026-03-01T00:00:00Z")
	if !meta.CreatedAt.Equal(wantCreated) {
		t.Errorf("yanlış CreatedAt: %v", meta.CreatedAt)
	}
}

func TestFetchRepoMetaNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchRepoMeta(context.Background(), srv.URL, "foo", "gone")
	if !errors.Is(err, ErrRepoNotFound) {
		t.Errorf("ErrRepoNotFound beklenirdi, geldi: %v", err)
	}
}

func TestFetchRepoMetaRateLimited(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		_, err := fetchRepoMeta(context.Background(), srv.URL, "foo", "bar")
		srv.Close()
		if !errors.Is(err, ErrGitHubRateLimited) {
			t.Errorf("HTTP %d için ErrGitHubRateLimited beklenirdi, geldi: %v", code, err)
		}
	}
}
